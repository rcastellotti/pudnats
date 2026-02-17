package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"strings"
)

const (
	tokenPrefix    = "PUD"
	tokenTotalLen  = 12
	tokenSlugChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
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

	token, err := generateToken()
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

func generateToken() (string, error) {
	suffixLen := tokenTotalLen - len(tokenPrefix)
	if suffixLen <= 0 {
		return "", errors.New("invalid token length configuration")
	}

	raw := make([]byte, suffixLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, 0, tokenTotalLen)
	out = append(out, tokenPrefix...)
	for _, b := range raw {
		out = append(out, tokenSlugChars[int(b)%len(tokenSlugChars)])
	}
	return string(out), nil
}
