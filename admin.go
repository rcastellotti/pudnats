package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	tokenPrefix    = "PUD"
	tokenTotalLen  = 12
	tokenSlugChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

func runAdmin(args []string) error {
	if len(args) == 0 {
		printAdminUsage()
		return nil
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printAdminUsage()
		return nil
	}
	switch args[0] {
	case "create-user":
		return runAdminCreateUser(args[1:])
	case "bootstrap-admin":
		return runAdminBootstrapAdmin(args[1:])
	default:
		printAdminUsage()
		return fmt.Errorf("unknown admin command: %s", args[0])
	}
}

func runAdminCreateUser(args []string) error {
	fs := flag.NewFlagSet("admin create-user", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s admin create-user --username <name> --token <admin-token> [options]\n\n", binName())
		fmt.Fprintln(fs.Output(), "Creates a user by calling the running API as an admin.")
		fmt.Fprintln(fs.Output())
		fmt.Fprintln(fs.Output(), "Options:")
		fs.PrintDefaults()
	}
	username := fs.String("username", "", "username to create")
	role := fs.String("role", "member", "role to assign: member|admin")
	apiURL := fs.String("api", "http://127.0.0.1:9173", "api base url")
	token := fs.String("token", "", "admin token used for API auth")
	timeout := fs.Duration("timeout", 10*time.Second, "http timeout")
	logPath := fs.String("log", defaultLogPath, "log target path ('-' for stdout only)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*username) == "" {
		return errors.New("--username is required")
	}
	if strings.TrimSpace(*token) == "" {
		return errors.New("--token is required")
	}
	*role = normalizeRole(*role)
	if !validRole(*role) {
		return errors.New("--role must be member or admin")
	}
	if _, err := url.ParseRequestURI(strings.TrimSpace(*apiURL)); err != nil {
		return errors.New("--api must be a valid URL, for example http://127.0.0.1:9173")
	}

	logger, closeLog, err := buildLogger(*logPath)
	if err != nil {
		return err
	}
	defer closeLog()
	return adminCreateUserViaAPI(logger, strings.TrimSpace(*apiURL), strings.TrimSpace(*token), strings.TrimSpace(*username), *role, *timeout)
}

func runAdminBootstrapAdmin(args []string) error {
	fs := flag.NewFlagSet("admin bootstrap-admin", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s admin bootstrap-admin --username <name> [options]\n\n", binName())
		fmt.Fprintln(fs.Output(), "Creates the initial admin account directly in DB. Allowed only when no admin user exists.")
		fmt.Fprintln(fs.Output())
		fmt.Fprintln(fs.Output(), "Options:")
		fs.PrintDefaults()
	}
	username := fs.String("username", "", "admin username to create")
	dbPath := fs.String("db", defaultDBPath, "sqlite db path")
	logPath := fs.String("log", defaultLogPath, "log target path ('-' for stdout only)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
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

	var admins int
	if err := app.db.QueryRow(`SELECT COUNT(1) FROM users WHERE role = 'admin'`).Scan(&admins); err != nil {
		return err
	}
	if admins > 0 {
		return errors.New("admin already exists; use `admin create-user` with an admin token")
	}

	uid, token, err := app.createUserWithRole(strings.TrimSpace(*username), "admin")
	if err != nil {
		return err
	}
	_ = app.logAction("admin_cli", "bootstrap", "bootstrap_admin", fmt.Sprintf("target_username=%s user_id=%d", strings.TrimSpace(*username), uid))
	fmt.Printf("created bootstrap admin: %s\n", *username)
	fmt.Printf("token (save now, cannot be retrieved later): %s\n", token)
	return nil
}

func printAdminUsage() {
	fmt.Printf("Usage: %s admin <subcommand> [options]\n\n", binName())
	fmt.Println("Subcommands:")
	fmt.Println("  create-user       Create a user via API (admin token required)")
	fmt.Println("  bootstrap-admin   Create first admin directly in DB (one-time setup)")
	fmt.Println()
	fmt.Printf("Try: %s admin create-user --help\n", binName())
	fmt.Printf("Try: %s admin bootstrap-admin --help\n", binName())
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

func adminCreateUserViaAPI(logger *log.Logger, apiURL, adminToken, username, role string, timeout time.Duration) error {
	body, err := json.Marshal(map[string]string{
		"username": username,
		"role":     role,
	})
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(apiURL, "/")+"/api/admin/users", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)

	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	respBody, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		return fmt.Errorf("api request failed: status=%d body=%s", res.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var payload struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return fmt.Errorf("failed to parse api response: %w", err)
	}

	logger.Printf("event=admin_cli action=create_user_via_api target_username=%s role=%s user_id=%d", payload.Username, payload.Role, payload.ID)
	fmt.Printf("created user: %s (role=%s)\n", payload.Username, payload.Role)
	fmt.Printf("token (save now, cannot be retrieved later): %s\n", payload.Token)
	return nil
}
