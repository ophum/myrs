package main

import (
	"embed"
	_ "embed"
	"errors"
	"flag"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-git/go-git/v5"
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
var logStorage *internal.SiteLogStorage
var phpFPMPoolConfTemplate *template.Template
var nginxSiteConfTemplate *template.Template

//go:embed php-fpm-pool.conf.tmpl
var phpFPMPoolConfTempl string

//go:embed nginx-site.conf.tmpl
var nginxSiteConfTempl string

//go:embed templates/*html
var templatesFS embed.FS

var siteDomain string

func main() {
	flag.StringVar(&siteDomain, "site-domain", "example.com", "site domain")
	flag.Parse()

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
	db.AutoMigrate(&Site{}, &Deploy{})
	logStorage, err = internal.NewSiteLogStorage()
	if err != nil {
		return err
	}
	defer logStorage.Close()

	phpFPMPoolConfTemplate, err = template.New("").Parse(phpFPMPoolConfTempl)
	if err != nil {
		return err
	}
	nginxSiteConfTemplate, err = template.New("").Parse(nginxSiteConfTempl)
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"statusTagColor": func(s int) string {
			switch s / 100 {
			case 2:
				return "is-success"
			case 3:
				return "is-info"
			case 4:
				return "is-warning"
			case 5:
				return "is-danger"
			default:
				return "is-light"
			}
		},
	}
	t := &Template{
		templates: template.Must(template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/*.html")),
	}
	e := echo.New()
	e.Renderer = t
	e.Use(middleware.Logger(), middleware.Recover())

	e.Use(session.Middleware(sessions.NewFilesystemStore("", []byte("secret"))))
	csrfConfig := middleware.DefaultCSRFConfig
	csrfConfig.TokenLookup = "form:_csrf"
	csrfConfig.Skipper = func(c echo.Context) bool {
		return c.Path() == "/fluentbit"
	}
	e.Use(middleware.CSRFWithConfig(csrfConfig))

	e.GET("/", index)
	e.GET("/create-site", getCreateSite)
	e.POST("/create-site", postCreateSite)
	e.POST("/create-deploy", postCreateDeploy)
	e.POST("/active-deploy", postActiveDeploy)
	e.POST("/sign-in", postSignIn)
	e.POST("/sign-out", postSignOut)
	e.POST("/fluentbit", logStorage.FluentbitHandler)
	e.GET("/log", getLog)
	e.GET("/api/minutely-request-counts", getAPIMinutelyRequestCount)

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
	var logs []*internal.NginxLog
	if isLogin {
		if err := db.Preload("Deploys", func(db *gorm.DB) *gorm.DB {
			return db.Order("id DESC")
		}).
			Where("id = ?", siteID).
			First(&site).Error; err != nil {
			return err
		}

		logs, err = logStorage.GetLogs(site.Name + "." + siteDomain)
		if err != nil {
			return err
		}
	}

	return c.Render(200, "index", map[string]any{
		"CSRF":           getCSRF(c),
		"IsLogin":        isLogin,
		"SiteID":         siteID,
		"Site":           site,
		"ActiveDeployID": site.ActiveDeployID,
		"SiteDomain":     siteDomain,
		"Logs":           logs,
	})
}

func getCreateSite(c echo.Context) error {
	return c.Render(200, "create-site", map[string]any{
		"CSRF":       getCSRF(c),
		"SiteDomain": siteDomain,
	})
}

type CreateSiteForm struct {
	SiteName   string `form:"site_name"`
	Password   string `form:"password"`
	RePassword string `form:"repassword"`
	RepoURL    string `form:"repo_url"`
	Path       string `form:"path"`
}

type Deploy struct {
	gorm.Model
	Revision string
	SiteID   uint
	Site     *Site
}

type Site struct {
	gorm.Model
	Name           string
	PasswordHash   string
	RepoURL        string
	Path           string
	ActiveDeployID uint
	Deploys        []*Deploy
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
		RepoURL:      formData.RepoURL,
		Path:         formData.Path,
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
		//wwwPath := filepath.Join(homePath, "www")
		//if err := os.Mkdir(wwwPath, os.ModePerm); err != nil {
		//	return err
		//}
		if err := os.Chown(homePath, int(uid), wwwDataGID); err != nil {
			return err
		}
		//if err := os.Chown(wwwPath, int(uid), wwwDataGID); err != nil {
		//	return err
		//}

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

func postCreateDeploy(c echo.Context) error {
	sess, err := getSession(c)
	if err != nil {
		return err
	}
	siteID, err := getSiteIDFromSession(sess)
	if err != nil {
		return err
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		var site Site
		if err := tx.Where("id = ?", siteID).First(&site).Error; err != nil {
			return err
		}

		deploy := Deploy{
			SiteID: site.ID,
		}
		if err := tx.Create(&deploy).Error; err != nil {
			return err
		}

		tmpPath := filepath.Join(
			os.TempDir(),
			site.Name,
			strconv.FormatUint(uint64(deploy.ID), 10),
		)
		repo, err := git.PlainClone(tmpPath, false, &git.CloneOptions{
			URL: site.RepoURL,
		})
		if err != nil {
			return err
		}

		head, err := repo.Head()
		if err != nil {
			return err
		}
		deploy.Revision = head.Hash().String()
		if err := tx.Where("id = ?", deploy.ID).Updates(&deploy).Error; err != nil {
			return err
		}

		srcPath := filepath.Join(tmpPath, "www", site.Path)
		dstPath := filepath.Join("/home", site.Name, "deploys", strconv.FormatUint(uint64(deploy.ID), 10))
		deploysDir := filepath.Join("/home", site.Name, "/deploys")
		if err := os.MkdirAll(deploysDir, os.ModePerm); err != nil {
			return err
		}
		if err := os.Rename(srcPath, dstPath); err != nil {
			return err
		}
		if err := os.RemoveAll(tmpPath); err != nil {
			return err
		}

		siteUser, err := user.Lookup(site.Name)
		if err != nil {
			return err
		}
		uid, err := strconv.Atoi(siteUser.Uid)
		if err != nil {
			return err
		}
		wwwDataGroup, err := user.LookupGroup("www-data")
		if err != nil {
			return err
		}
		gid, err := strconv.Atoi(wwwDataGroup.Gid)
		if err != nil {
			return err
		}
		if err := filepath.Walk(deploysDir, func(path string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if err := os.Chown(path, uid, gid); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	return c.Redirect(http.StatusFound, "/")
}

type ActiveDeployForm struct {
	DeployID uint `form:"deploy_id"`
}

func postActiveDeploy(c echo.Context) error {
	var formData ActiveDeployForm
	if err := c.Bind(&formData); err != nil {
		return err
	}

	sess, err := getSession(c)
	if err != nil {
		return err
	}
	siteID, err := getSiteIDFromSession(sess)
	if err != nil {
		return err
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		var site Site
		if err := tx.Clauses(clause.Locking{
			Strength: "UPDATE",
		}).
			Where("id = ?", siteID).First(&site).Error; err != nil {
			return err
		}

		var deploy Deploy
		if err := tx.Where("id = ? AND site_id = ?", formData.DeployID, site.ID).
			First(&deploy).Error; err != nil {
			return err
		}

		site.ActiveDeployID = formData.DeployID
		if err := tx.Where("id = ?", site.ID).Updates(&site).Error; err != nil {
			return err
		}
		deployIDStr := strconv.FormatUint(uint64(deploy.ID), 10)
		wwwPath := filepath.Join("/home", site.Name, "www")
		tmpWww := wwwPath + ".tmp"
		if err := os.Symlink(
			filepath.Join("./deploys/", deployIDStr),
			tmpWww,
		); err != nil {
			return err
		}

		if err := os.Rename(tmpWww, wwwPath); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/")
}

func getLog(c echo.Context) error {
	sess, err := getSession(c)
	if err != nil {
		return err
	}
	siteID, err := getSiteIDFromSession(sess)
	if err != nil {
		return c.Redirect(http.StatusFound, "/")
	}

	var site Site
	if err := db.Where("id = ?", siteID).First(&site).Error; err != nil {
		return err
	}
	logs, err := logStorage.GetLogs(site.Name + "." + siteDomain)
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "log", map[string]any{
		"Site":       site,
		"SiteDomain": siteDomain,
		"Logs":       logs,
	})
}

func getAPIMinutelyRequestCount(c echo.Context) error {
	sess, err := getSession(c)
	if err != nil {
		return err
	}
	siteID, err := getSiteIDFromSession(sess)
	if err != nil {
		return c.Redirect(http.StatusFound, "/")
	}

	var site Site
	if err := db.Where("id = ?", siteID).First(&site).Error; err != nil {
		return err
	}
	times, requestCounts, err := logStorage.GetMinutelyRequestCount(site.Name + "." + siteDomain)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"Times":         times,
		"RequestCounts": requestCounts,
	})
}
