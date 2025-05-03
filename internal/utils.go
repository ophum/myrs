package internal

import (
	"database/sql"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"

	_ "github.com/go-sql-driver/mysql"
)

func execCommand(command string, args []string) error {
	cmd := exec.Command(command, args...)
	output, err := cmd.CombinedOutput()
	log.Println(string(output))
	if err != nil {
		return err
	}
	return nil
}

func systemctlReload(service string) error {
	command := "systemctl"
	args := []string{
		"reload",
		service,
	}
	return execCommand(command, args)
}

func Useradd(name string, uid uint) error {
	command := "useradd"
	args := []string{
		"-m", "-s", "/bin/bash",
		"-u", strconv.FormatInt(int64(uid), 10),
		name,
	}
	return execCommand(command, args)
}

func Userdel(name string) error {
	command := "userdel"
	args := []string{
		"-r", name,
	}
	return execCommand(command, args)
}

func ReloadPHPFPM() error {
	return systemctlReload("php8.3-fpm")
}

func ReloadNginx() error {
	return systemctlReload("nginx")
}

func getDB() (*sql.DB, error) {
	return sql.Open("mysql", "root:@unix(/var/run/mysqld/mysqld.sock)/")
}

func CreateDatabase(name string) error {
	db, err := getDB()
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec("CREATE DATABASE `" + name + "`"); err != nil {
		return err
	}
	return nil
}

func CreateDatabaseUser(name string) error {
	db, err := getDB()
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec("CREATE USER '" + name + "'@'localhost' IDENTIFIED WITH auth_socket"); err != nil {
		return err
	}
	if _, err := db.Exec("GRANT ALL ON `" + name + "`.* TO '" + name + "'@'localhost'"); err != nil {
		return err
	}
	return nil
}

func DropDatabase(name string) error {
	db, err := getDB()
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec("DROP DATABASE `" + name + "`"); err != nil {
		return err
	}
	return nil
}

func DropDatabaseUser(name string) error {
	db, err := getDB()
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec("DROP USER '" + name + "'"); err != nil {
		return err
	}
	return nil
}

func WriteFile(path string, fn func(w io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := fn(f); err != nil {
		if err := os.Remove(path); err != nil {
			log.Println(err)
		}
		return err
	}
	return nil
}
