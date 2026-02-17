package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"strings"
)

func runAdmin(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: admin create-user --username <name> [--db path] [--log path]")
	}
	if args[0] != "create-user" {
		return fmt.Errorf("unknown admin command: %s", args[0])
	}

	fs := flag.NewFlagSet("admin create-user", flag.ContinueOnError)
	username := fs.String("username", "", "username to create")
	dbPath := fs.String("db", defaultDBPath, "sqlite db path")
	logPath := fs.String("log", defaultLogPath, "log file path")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*username) == "" {
		return errors.New("--username is required")
	}

	logger, closeLog, err := buildLogger(*logPath)
	if err != nil {
		return err
	}
	defer closeLog()

	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	app := &App{db: db, logger: logger}
	if err := app.initSchema(); err != nil {
		return err
	}

	token, err := generateToken(32)
	if err != nil {
		return err
	}
	hash := hashToken(token)

	res, err := app.db.Exec(`INSERT INTO users(username, token_hash, created_at) VALUES(?, ?, ?)`, strings.TrimSpace(*username), hash, nowUTC())
	if err != nil {
		return err
	}
	uid, _ := res.LastInsertId()
	_ = app.logAction("admin_cli", "admin", "create_user", fmt.Sprintf("target_username=%s user_id=%d", *username, uid))

	fmt.Printf("created user: %s\n", *username)
	fmt.Printf("token (save now, cannot be retrieved later): %s\n", token)
	return nil
}

func generateToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
