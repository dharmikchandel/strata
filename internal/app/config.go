// Package app wires Strata's components into one running server: the S3
// storage, the manifest, the ingest buffer, the gRPC server and the background
// compactor, and shuts them down in the order that loses no data.
package app

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dharmikchandel/strata/internal/compact"
	"github.com/dharmikchandel/strata/internal/ingest"
	"github.com/dharmikchandel/strata/internal/storage"
)

// Config is everything needed to run a server.
type Config struct {
	// ListenAddr is the gRPC address. It defaults to loopback only, because
	// there is no TLS or authentication yet: binding to all interfaces must be
	// a deliberate choice.
	ListenAddr   string
	ManifestPath string

	S3 storage.S3Config
	// CreateBucket creates the bucket at startup if it doesn't exist. Handy
	// for local development; off by default because silently creating a bucket
	// in production would hide a misconfigured name.
	CreateBucket bool

	BufferBytes int
	BufferAge   time.Duration

	CompactEnabled     bool
	CompactInterval    time.Duration
	CompactSmallBytes  int64
	CompactTargetBytes int64
	OrphanGrace        time.Duration

	MaxRecvMsgBytes int
	// ShutdownGrace is how long Shutdown waits for in-flight gRPC streams to
	// finish before cutting them off.
	ShutdownGrace time.Duration

	LogLevel string

	// Test hooks: if set, Storage is used instead of connecting to S3, and
	// Logger instead of one built from LogLevel.
	Storage storage.Storage
	Logger  *slog.Logger
}

// HelpRequested is returned by ParseConfig for -h/-help. It is not a failure:
// the caller should print Usage and exit successfully.
type HelpRequested struct{ Usage string }

func (h *HelpRequested) Error() string { return h.Usage }
func (h *HelpRequested) Unwrap() error { return flag.ErrHelp }

// ParseConfig reads configuration from command-line flags, with environment
// variables as defaults: a flag beats an environment variable, which beats the
// built-in default. Secrets (S3 keys) are best passed through the environment,
// since command-line arguments are visible to other users of the machine.
func ParseConfig(args []string, getenv func(string) string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("strata", flag.ContinueOnError)
	var usage bytes.Buffer
	fs.SetOutput(&usage)

	// A malformed environment variable is an error, not something to ignore:
	// silently falling back to the default would hide a typo like
	// STRATA_BUFFER_AGE=5sec until it caused trouble in production.
	var envErrs []string
	badEnv := func(env, v string, err error) {
		envErrs = append(envErrs, fmt.Sprintf("%s=%q: %v", env, v, err))
	}

	str := func(p *string, name, env, def, help string) {
		if v := getenv(env); v != "" {
			def = v
		}
		fs.StringVar(p, name, def, help+" [env "+env+"]")
	}
	boolean := func(p *bool, name, env string, def bool, help string) {
		if v := getenv(env); v != "" {
			if b, err := strconv.ParseBool(v); err != nil {
				badEnv(env, v, err)
			} else {
				def = b
			}
		}
		fs.BoolVar(p, name, def, help+" [env "+env+"]")
	}
	dur := func(p *time.Duration, name, env string, def time.Duration, help string) {
		if v := getenv(env); v != "" {
			if d, err := time.ParseDuration(v); err != nil {
				badEnv(env, v, err)
			} else {
				def = d
			}
		}
		fs.DurationVar(p, name, def, help+" [env "+env+"]")
	}
	num := func(p *int64, name, env string, def int64, help string) {
		if v := getenv(env); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err != nil {
				badEnv(env, v, err)
			} else {
				def = n
			}
		}
		fs.Int64Var(p, name, def, help+" [env "+env+"]")
	}

	str(&c.ListenAddr, "listen", "STRATA_LISTEN", "127.0.0.1:7070", "gRPC listen address (no TLS/auth: keep it on loopback or a private network)")
	str(&c.ManifestPath, "manifest", "STRATA_MANIFEST", "./data/manifest.db", "path of the SQLite manifest file (must persist across restarts)")
	str(&c.S3.Endpoint, "s3-endpoint", "STRATA_S3_ENDPOINT", "", "S3 API URL; empty means AWS S3")
	str(&c.S3.Region, "s3-region", "STRATA_S3_REGION", "us-east-1", "S3 region")
	str(&c.S3.Bucket, "s3-bucket", "STRATA_S3_BUCKET", "strata", "bucket holding segments")
	str(&c.S3.AccessKey, "s3-access-key", "STRATA_S3_ACCESS_KEY", "", "S3 access key (prefer the environment variable)")
	// The secret is deliberately NOT given the environment value as the flag's
	// default: the usage text prints defaults, which would print the secret.
	// It is read from the environment after parsing instead.
	fs.StringVar(&c.S3.SecretKey, "s3-secret-key", "", "S3 secret key (prefer the environment variable) [env STRATA_S3_SECRET_KEY]")
	boolean(&c.S3.PathStyle, "s3-path-style", "STRATA_S3_PATH_STYLE", false, "path-style bucket addressing; defaults to on when an endpoint is set, since self-hosted S3 servers need it")
	boolean(&c.CreateBucket, "s3-create-bucket", "STRATA_S3_CREATE_BUCKET", false, "create the bucket at startup if missing")

	var bufBytes int64
	num(&bufBytes, "buffer-bytes", "STRATA_BUFFER_BYTES", ingest.DefaultMaxBytes, "seal a segment when buffered data reaches this size")
	dur(&c.BufferAge, "buffer-age", "STRATA_BUFFER_AGE", ingest.DefaultMaxAge, "seal a segment when its oldest entry has waited this long")

	var noCompact bool
	boolean(&noCompact, "no-compact", "STRATA_NO_COMPACT", false, "disable background compaction")
	dur(&c.CompactInterval, "compact-interval", "STRATA_COMPACT_INTERVAL", compact.DefaultInterval, "how often to look for segments to merge")
	num(&c.CompactSmallBytes, "compact-small-bytes", "STRATA_COMPACT_SMALL_BYTES", compact.DefaultSmallBytes, "only segments smaller than this are merged")
	num(&c.CompactTargetBytes, "compact-target-bytes", "STRATA_COMPACT_TARGET_BYTES", compact.DefaultTargetBytes, "upper bound on the size of one merge")
	dur(&c.OrphanGrace, "orphan-grace", "STRATA_ORPHAN_GRACE", compact.DefaultOrphanGrace, "unrecorded segment files younger than this are never deleted; must exceed the slowest seal")

	var maxRecv int64
	num(&maxRecv, "max-recv-bytes", "STRATA_MAX_RECV_BYTES", ingest.DefaultMaxRecvMsgBytes, "largest gRPC request accepted")
	dur(&c.ShutdownGrace, "shutdown-grace", "STRATA_SHUTDOWN_GRACE", 10*time.Second, "how long to let in-flight streams finish at shutdown")
	str(&c.LogLevel, "log-level", "STRATA_LOG_LEVEL", "info", "debug, info, warn or error")

	if len(envErrs) > 0 {
		return c, errors.New("invalid environment: " + strings.Join(envErrs, "; "))
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return c, &HelpRequested{Usage: usage.String()}
		}
		return c, fmt.Errorf("%w\n%s", err, usage.String())
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if c.S3.SecretKey == "" {
		c.S3.SecretKey = getenv("STRATA_S3_SECRET_KEY")
	}
	// Path-style addressing defaults to on whenever a custom endpoint is used,
	// unless the user said otherwise (flag or environment).
	pathStyleSet := getenv("STRATA_S3_PATH_STYLE") != ""
	fs.Visit(func(f *flag.Flag) { pathStyleSet = pathStyleSet || f.Name == "s3-path-style" })
	if !pathStyleSet && c.S3.Endpoint != "" {
		c.S3.PathStyle = true
	}
	c.BufferBytes = int(bufBytes)
	c.MaxRecvMsgBytes = int(maxRecv)
	c.CompactEnabled = !noCompact
	return c, c.Validate()
}

// Validate rejects configurations that can't work, with an error that says
// which setting is wrong, so a bad deployment fails at startup, not later.
func (c Config) Validate() error {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if c.ListenAddr == "" {
		bad("listen address is empty")
	}
	if c.ManifestPath == "" {
		bad("manifest path is empty")
	}
	if c.Storage == nil && c.S3.Bucket == "" {
		bad("s3 bucket is empty")
	}
	if (c.S3.AccessKey == "") != (c.S3.SecretKey == "") {
		bad("s3 access key and secret key must be set together")
	}
	if c.BufferBytes <= 0 || c.BufferAge <= 0 {
		bad("buffer size and age must be positive")
	}
	if c.MaxRecvMsgBytes <= 0 {
		bad("max-recv-bytes must be positive")
	}
	if c.CompactEnabled {
		if c.CompactInterval <= 0 || c.CompactSmallBytes <= 0 || c.CompactTargetBytes <= 0 {
			bad("compaction interval and sizes must be positive")
		}
		if c.CompactTargetBytes < c.CompactSmallBytes {
			bad("compact-target-bytes must be at least compact-small-bytes")
		}
	}
	if c.OrphanGrace <= 0 {
		bad("orphan-grace must be positive")
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error", "":
	default:
		bad("log-level %q is not one of debug, info, warn, error", c.LogLevel)
	}
	if len(problems) > 0 {
		return errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return nil
}

func (c Config) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.LogLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
