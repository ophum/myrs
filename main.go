package main

import (
	_ "embed"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/ophum/myrs/internal"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var db *gorm.DB
var phpFPMPoolConfTemplate *template.Template
var nginxSiteConfTemplate *template.Template

//go:embed php-fpm-pool.conf.tmpl
var phpFPMPoolConfTempl string

//go:embed nginx-site.conf.tmpl
var nginxSiteConfTempl string

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}

func run() error {
	var err error
	db, err = gorm.Open(sqlite.Open("gorm.db"), &gorm.Config{})
	if err != nil {
		return err
	}
	db.AutoMigrate(&Site{})

	phpFPMPoolConfTemplate, err = template.New("").Parse(phpFPMPoolConfTempl)
	if err != nil {
		return err
	}
	nginxSiteConfTemplate, err = template.New("").Parse(nginxSiteConfTempl)
	t := &Template{
		templates: template.Must(template.ParseGlob("templates/*.html")),
	}
	e := echo.New()
	e.Renderer = t
	e.Use(middleware.Logger(), middleware.Recover())
	e.Use(session.Middleware(sessions.NewFilesystemStore("", []byte("secret"))))
	csrfConfig := middleware.DefaultCSRFConfig
	csrfConfig.TokenLookup = "form:_csrf"
	e.Use(middleware.CSRFWithConfig(csrfConfig))

	e.GET("/", index)
	e.GET("/create-site", getCreateSite)
	e.POST("/create-site", postCreateSite)
	e.POST("/sign-in", postSignIn)
	e.POST("/sign-out", postSignOut)

	if err := e.Start(":8080"); err != nil {
		return err
	}
	return nil
}

type Template struct {
	templates *template.Template
}

func (t *Template) Render(w io.Writer, name string, data any, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

func getSession(c echo.Context) (*sessions.Session, error) {
	return session.Get("session", c)
}

func saveSession(sess *sessions.Session, c echo.Context) error {
	sess.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
	}
	return sess.Save(c.Request(), c.Response())
}

func deleteSession(c echo.Context) error {
	sess, err := getSession(c)
	if err != nil {
		return err
	}

	sess.Options.MaxAge = -1
	return sess.Save(c.Request(), c.Response())
}

func getCSRF(c echo.Context) string {
	return c.Get("csrf").(string)
}

func getSiteIDFromSession(sess *sessions.Session) (uint, error) {
	siteID, ok := sess.Values["site_id"].(uint)
	if !ok {
		return 0, errors.New("not set")
	}
	return siteID, nil
}

func index(c echo.Context) error {
	sess, err := getSession(c)
	if err != nil {
		return err
	}

	siteID, err := getSiteIDFromSession(sess)
	isLogin := err == nil
	var site Site
	if isLogin {
		if err := db.Where("id = ?", siteID).First(&site).Error; err != nil {
			return err
		}
	}

	return c.Render(200, "index", map[string]any{
		"CSRF":    getCSRF(c),
		"IsLogin": isLogin,
		"SiteID":  siteID,
		"Site":    site,
	})
}

func getCreateSite(c echo.Context) error {
	return c.Render(200, "create-site", map[string]any{
		"CSRF": getCSRF(c),
	})
}

type CreateSiteForm struct {
	SiteName   string `form:"site_name"`
	Password   string `form:"password"`
	RePassword string `form:"repassword"`
}

type Site struct {
	gorm.Model
	Name         string
	PasswordHash string
}

func postCreateSite(c echo.Context) error {
	var formData CreateSiteForm
	if err := c.Bind(&formData); err != nil {
		return err
	}

	log.Println(formData)

	sess, err := getSession(c)
	if err != nil {
		return err
	}

	if formData.Password != formData.RePassword {
		return errors.New("validation error, invalid repassword")
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(formData.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	site := Site{
		Name:         formData.SiteName,
		PasswordHash: string(passwordHash),
	}
	rollbackFuncs := []func() error{}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{
			Strength: "UPDATE",
		}).Find(&Site{}).Error; err != nil {
			return err
		}

		var exists bool
		if err := tx.Model(&Site{}).
			Where("name = ?", formData.SiteName).
			Select("COUNT(*)>0").
			First(&exists).Error; err != nil {
			return err
		}
		if exists {
			return errors.New("already exists")
		}

		if err := tx.Create(&site).Error; err != nil {
			return err
		}

		uid := site.ID + 10000

		if err := internal.Useradd(site.Name, uid); err != nil {
			return err
		}
		rollbackFuncs = append(rollbackFuncs, func() error {
			log.Println("rollback userdel", site.Name)
			return internal.Userdel(site.Name)
		})

		wwwDataGroup, err := user.LookupGroup("www-data")
		if err != nil {
			return err
		}
		wwwDataGID, err := strconv.Atoi(wwwDataGroup.Gid)
		if err != nil {
			return err
		}
		homePath := filepath.Join("/home", site.Name)
		wwwPath := filepath.Join(homePath, "www")
		if err := os.Mkdir(wwwPath, os.ModePerm); err != nil {
			return err
		}
		if err := os.Chown(homePath, int(uid), wwwDataGID); err != nil {
			return err
		}
		if err := os.Chown(wwwPath, int(uid), wwwDataGID); err != nil {
			return err
		}

		poolConfFilePath := filepath.Join("/etc/php/8.3/fpm/pool.d/", site.Name+".conf")
		if err := internal.WriteFile(poolConfFilePath, func(w io.Writer) error {
			return phpFPMPoolConfTemplate.Execute(w, map[string]any{
				"SiteName": site.Name,
			})
		}); err != nil {
			return err
		}
		rollbackFuncs = append(rollbackFuncs, func() error {
			log.Println("rollback create poolConfFile", site.Name)
			time.Sleep(time.Second * 3) // 正常処理でphp8.3-fpmをreloadしたときのプロセスがファイルを開こうとするタイミングで消されるので、reloadが終わってから消すようにする。とりあえず数秒待つことで対応。本当はちゃんと状態を見てやったほうがいいがどの値を見ればいいかわからないので。
			if err := os.Remove(poolConfFilePath); err != nil {
				return err
			}
			return internal.ReloadPHPFPM()
		})

		nginxSiteConfFilePath := filepath.Join("/etc/nginx/conf.d/", site.Name+".conf")
		if err := internal.WriteFile(nginxSiteConfFilePath, func(w io.Writer) error {
			return nginxSiteConfTemplate.Execute(w, map[string]any{
				"SiteName": site.Name,
			})
		}); err != nil {
			return err
		}
		rollbackFuncs = append(rollbackFuncs, func() error {
			log.Println("rollback create nginxSiteConfFile", site.Name)
			if err := os.Remove(nginxSiteConfFilePath); err != nil {
				return err
			}
			return internal.ReloadNginx()
		})

		if err := internal.ReloadPHPFPM(); err != nil {
			return err
		}
		if err := internal.ReloadNginx(); err != nil {
			return err
		}
		if err := internal.CreateDatabase(site.Name); err != nil {
			return err
		}
		rollbackFuncs = append(rollbackFuncs, func() error {
			log.Println("rollback create database", site.Name)
			return internal.DropDatabase(site.Name)
		})
		if err := internal.CreateDatabaseUser(site.Name); err != nil {
			return err
		}
		rollbackFuncs = append(rollbackFuncs, func() error {
			log.Println("rollback create database user", site.Name)
			return internal.DropDatabaseUser(site.Name)
		})
		return nil
	}); err != nil {
		for i := len(rollbackFuncs) - 1; i >= 0; i-- {
			if err := rollbackFuncs[i](); err != nil {
				log.Println(err)
			}
		}
		return err
	}

	sess.Values["site_id"] = site.ID
	if err := saveSession(sess, c); err != nil {
		return err
	}

	return c.Redirect(http.StatusFound, "/")
}

type SignInForm struct {
	SiteName string `form:"site_name"`
	Password string `form:"password"`
}

func postSignIn(c echo.Context) error {
	var formData SignInForm
	if err := c.Bind(&formData); err != nil {
		return err
	}

	var site Site
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{
			Strength: "SHARE",
			Table:    clause.Table{Name: clause.CurrentTable},
		}).Where("name = ?", formData.SiteName).First(&site).Error; err != nil {
			return err
		}

		if err := bcrypt.CompareHashAndPassword([]byte(site.PasswordHash), []byte(formData.Password)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	sess, err := getSession(c)
	if err != nil {
		return err
	}

	sess.Values["site_id"] = site.ID

	if err := saveSession(sess, c); err != nil {
		return err
	}

	return c.Redirect(http.StatusFound, "/")
}

func postSignOut(c echo.Context) error {
	if err := deleteSession(c); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/")
}
