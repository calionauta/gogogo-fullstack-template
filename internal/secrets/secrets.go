// SCOPE:layer=infra,removal=core — Secret management with age (age-encryption.org)
// Package secrets decrypts a per-project environment file using age
// (https://age-encryption.org). Designed to make "no vault, no cloud" the
// default for single-binary Go apps:
//
//   - The project owner runs bin/init-secrets once to generate an age
//     keypair and write a project-scoped env file at
//     ~/.secrets/<project>.env.age (chmod 600).
//   - The deploy machine / CI gets AGE_SECRET_KEY in its environment
//     (the X25519 secret from the keypair).
//   - At startup, Load decrypts the file and re-exports KEY=value lines
//     into the process environment so config.Load can read them via
//     os.Getenv.
//
// The project name is passed in by the caller (config.Load) and is
// derived from APP_NAME env or the binary name. There is no implicit
// "gogogo-fullstack-template" name baked in — a user who renames their project
// gets a separate secrets file.
package secrets

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"

	"filippo.io/age"
)

// Load decrypts ~/.secrets/<projectName>.env.age into environment
// variables. Silent skip if the file doesn't exist or AGE_SECRET_KEY
// is unset. A warning is logged on parse/decrypt failure (still
// non-fatal so the app can boot in dev).
func Load(projectName string) {
	if projectName == "" {
		return
	}
	key := os.Getenv("AGE_SECRET_KEY")
	if key == "" {
		return
	}

	usr, err := user.Current()
	if err != nil {
		return
	}

	secretsFile := filepath.Join(usr.HomeDir, ".secrets", projectName+".env.age")
	data, err := os.ReadFile(secretsFile)
	if err != nil {
		return
	}

	identity, err := age.ParseX25519Identity(key)
	if err != nil {
		slog.Default().Warn("secrets: invalid age key (skip)", "project", projectName, "error", err)
		return
	}

	out, err := age.Decrypt(bytes.NewReader(data), identity)
	if err != nil {
		slog.Default().Warn("secrets: decrypt failed (skip)", "project", projectName, "error", err)
		return
	}
	decrypted, err := io.ReadAll(out)
	if err != nil {
		slog.Default().Warn("secrets: read failed (skip)", "project", projectName, "error", err)
		return
	}

	for _, line := range parseEnvLines(string(decrypted)) {
		// Plain os.Setenv: secrets are decrypted, then re-exported into
		// the process environment so the rest of the app reads them via
		// os.Getenv in config.Load.
		os.Setenv(line.Key, line.Value)
	}
}

type envLine struct {
	Key   string
	Value string
}

// parseEnvLines parses KEY=value lines from s. Handles comments (#) and
// blank lines. Values are not quoted/escaped beyond leading/trailing
// whitespace; that's the contract init-secrets documents.
func parseEnvLines(s string) []envLine {
	var lines []envLine
	var buf []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if line := parseLine(string(buf)); line != nil {
				lines = append(lines, *line)
			}
			buf = buf[:0]
		} else {
			buf = append(buf, s[i])
		}
	}
	if line := parseLine(string(buf)); line != nil {
		lines = append(lines, *line)
	}
	return lines
}

func parseLine(s string) *envLine {
	s = trimSpace(s)
	if s == "" || s[0] == '#' {
		return nil
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			key := trimSpace(s[:i])
			val := trimSpace(s[i+1:])
			if key != "" {
				return &envLine{Key: key, Value: val}
			}
			return nil
		}
	}
	return nil
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
