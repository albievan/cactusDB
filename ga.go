package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Google Analytics (see GOOGLE_ANALYTICS.md). Each cactusdb server has its
// own Measurement ID, set in ☰ → Settings (or $CACTUSDB_GA_ID, which wins).
// Nothing is sent unless a person opts in, in their browser; the page then
// loads Google's tag itself, so a blocked Google never holds anything up.
//
// Command-line runs are sent from here with the Measurement Protocol, which
// needs an API secret, and only when "send command-line runs" is switched on
// for the server. They are also always recorded in the usage log.

const (
	gaIDEnv     = "CACTUSDB_GA_ID"
	gaSecretEnv = "CACTUSDB_GA_API_SECRET"

	gaIDKey       = "ga_id"
	gaSecretKey   = "ga_api_secret" // encrypted
	gaCLIKey      = "ga_cli"        // "1": send command-line runs
	gaClientIDKey = "ga_cli_client_id"

	gaTimeout = 2 * time.Second // the most a command-line run waits for Google
)

var gaIDRe = regexp.MustCompile(`^G-[A-Z0-9]{4,20}$`)

// gaConfig is the server's Google Analytics setup.
type gaConfig struct {
	ID, Secret      string
	IDEnv, SecEnv   bool // from the environment (which wins over the setting)
	CLI             bool
	secretSetInMeta bool
}

func loadGAConfig(st *Store) gaConfig {
	var c gaConfig
	if st != nil {
		c.ID, _ = st.meta(gaIDKey)
		if v, _ := st.meta(gaSecretKey); v != "" {
			if plain, err := st.box.open(gaSecretKey, v); err == nil {
				c.Secret, c.secretSetInMeta = plain, true
			}
		}
		v, _ := st.meta(gaCLIKey)
		c.CLI = v == "1"
	}
	if v := strings.TrimSpace(os.Getenv(gaIDEnv)); v != "" {
		c.ID, c.IDEnv = v, true
	}
	if v := strings.TrimSpace(os.Getenv(gaSecretEnv)); v != "" {
		c.Secret, c.SecEnv = v, true
	}
	return c
}

// gaSettings is what the page gets: never the secret.
func (c gaConfig) json() map[string]any {
	return map[string]any{"gaId": c.ID, "gaIdEnv": c.IDEnv, "gaSecretSet": c.Secret != "", "gaSecretEnv": c.SecEnv, "gaCli": c.CLI}
}

// saveGASettings saves what the Settings dialog sent; nil leaves a value.
func saveGASettings(st *Store, id, secret *string, clearSecret, cli *bool) error {
	if id != nil {
		v := strings.ToUpper(strings.TrimSpace(*id))
		if v != "" && !gaIDRe.MatchString(v) {
			return fmt.Errorf("%q is not a Google Analytics Measurement ID: it looks like G-XXXXXXXXXX (Admin → Data streams → your stream)", *id)
		}
		if err := st.setMeta(gaIDKey, v); err != nil {
			return err
		}
	}
	if clearSecret != nil && *clearSecret {
		if err := st.setMeta(gaSecretKey, ""); err != nil {
			return err
		}
	} else if secret != nil && strings.TrimSpace(*secret) != "" {
		if err := st.setMeta(gaSecretKey, st.box.seal(gaSecretKey, strings.TrimSpace(*secret))); err != nil {
			return err
		}
	}
	if cli != nil {
		v := ""
		if *cli {
			v = "1"
		}
		if err := st.setMeta(gaCLIKey, v); err != nil {
			return err
		}
	}
	return nil
}

// cliRun is one command-line run, for the usage log and Google Analytics.
type cliRun struct {
	o        cliOptions
	start    time.Time
	err      error
	function string // export, export DDL, backup, show tables
}

func (c cliRun) gaName() string {
	return "cli_" + strings.ReplaceAll(strings.ToLower(c.function), " ", "_")
}

func cliFunction(o cliOptions) string {
	switch {
	case o.Show != "":
		return "show tables"
	case o.DDL != "":
		return "export DDL"
	case o.Backup:
		return "backup"
	}
	return "export"
}

// recordCLI records a command-line run in the usage log of st and, when the
// server sends command-line runs, in Google Analytics. Nothing here can make
// the run fail; Google gets at most gaTimeout.
func recordCLI(st *Store, run cliRun) {
	if st == nil {
		return
	}
	d := time.Since(run.start)
	result := "ok"
	detail := cliDetail(run.o)
	if run.err != nil {
		result = "failed"
		detail += " | FAILED: " + run.err.Error()
	}
	host, _ := os.Hostname()
	who := ""
	if u, err := user.Current(); err == nil {
		who = u.Username + "@"
	}
	ev := usageEvent{at: run.start, source: "cli", function: "cli: " + run.function, detail: clip(detail, 2000),
		ip: "local", connection: cliConnName(run.o.ConnParams), engine: run.o.Engine,
		status: map[bool]int{true: 200, false: 500}[run.err == nil], ms: d.Milliseconds(),
		agent: fmt.Sprintf("cactusdb-cli/%s (%s/%s; %s%s)", version, runtime.GOOS, runtime.GOARCH, who, host)}
	if _, err := st.DB().Exec(usageSchema); err == nil {
		insertUsage(st.DB(), []usageEvent{ev})
	}

	c := loadGAConfig(st)
	if !c.CLI || c.ID == "" || c.Secret == "" {
		return
	}
	params := map[string]any{"engine": run.o.Engine, "result": result, "duration_bucket": durationBucket(d),
		"app_version": version, "engagement_time_msec": 1}
	if run.function == "export" {
		params["format"] = run.o.Format
		params["tables_bucket"] = tablesBucket(run.o.Tables)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gaTimeout)
	defer cancel()
	sendMeasurement(ctx, c.ID, c.Secret, gaClientID(st), run.gaName(), params)
}

// cliDetail describes a run like the log does, without the password.
func cliDetail(o cliOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", cliConnName(o.ConnParams))
	switch {
	case o.Show != "":
		fmt.Fprintf(&b, ", show %s", o.Show)
	case o.DDL != "":
		fmt.Fprintf(&b, ", DDL of %s", o.DDL)
	case o.Backup:
		b.WriteString(", backup")
	default:
		fmt.Fprintf(&b, ", tables %s as %s to %s", o.Tables, o.Format, o.ExportDir)
	}
	return b.String()
}

func durationBucket(d time.Duration) string {
	switch {
	case d < time.Second:
		return "<1s"
	case d < 10*time.Second:
		return "1-10s"
	case d < time.Minute:
		return "10-60s"
	}
	return ">60s"
}

func tablesBucket(tables string) string {
	if strings.EqualFold(strings.TrimSpace(tables), "all") {
		return "all"
	}
	switch n := len(strings.Split(tables, ",")); {
	case n == 1:
		return "1"
	case n <= 10:
		return "2-10"
	}
	return ">10"
}

// gaClientID is this machine's id for Google Analytics, made once.
func gaClientID(st *Store) string {
	if v, _ := st.meta(gaClientIDKey); v != "" {
		return v
	}
	var b [8]byte
	rand.Read(b[:])
	id := fmt.Sprintf("%d.%d", binary.BigEndian.Uint32(b[:4]), time.Now().Unix())
	st.setMeta(gaClientIDKey, id)
	return id
}

// gaEndpoint is Google's Measurement Protocol endpoint (a variable for tests).
var gaEndpoint = "https://www.google-analytics.com/mp/collect"

// sendMeasurement sends one event; errors are ignored: Google being blocked
// or slow must never matter. A proxy in $HTTPS_PROXY is used.
func sendMeasurement(ctx context.Context, id, secret, clientID, name string, params map[string]any) error {
	body, _ := json.Marshal(map[string]any{"client_id": clientID,
		"events": []map[string]any{{"name": name, "params": params}}})
	u := gaEndpoint + "?" + url.Values{"measurement_id": {id}, "api_secret": {secret}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: gaTimeout}).Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return errors.New(res.Status)
	}
	return nil
}
