package app

import (
	"errors"
	"flag"
	"strings"
	"testing"
	"time"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := ParseConfig(nil, env())
	if err != nil {
		t.Fatal(err)
	}
	// Safe by default: loopback only, because there is no TLS or auth.
	if c.ListenAddr != "127.0.0.1:7070" {
		t.Errorf("default listen address %q must be loopback", c.ListenAddr)
	}
	if !c.CompactEnabled || c.CreateBucket || c.S3.PathStyle || c.S3.Bucket != "strata" || c.S3.Endpoint != "" {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

func TestEnvironmentThenFlagPrecedence(t *testing.T) {
	e := env("STRATA_LISTEN", "0.0.0.0:1111", "STRATA_S3_BUCKET", "from-env", "STRATA_BUFFER_AGE", "7s", "STRATA_S3_CREATE_BUCKET", "true")
	c, err := ParseConfig(nil, e)
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != "0.0.0.0:1111" || c.S3.Bucket != "from-env" || c.BufferAge != 7*time.Second || !c.CreateBucket {
		t.Fatalf("environment ignored: %+v", c)
	}
	c, err = ParseConfig([]string{"-listen", "127.0.0.1:2222", "-s3-bucket", "from-flag", "-buffer-age", "9s"}, e)
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != "127.0.0.1:2222" || c.S3.Bucket != "from-flag" || c.BufferAge != 9*time.Second {
		t.Fatalf("flags must beat the environment: %+v", c)
	}
}

func TestPathStyleFollowsEndpointUnlessOverridden(t *testing.T) {
	cases := []struct {
		name string
		args []string
		e    func(string) string
		want bool
	}{
		{"no endpoint", nil, env(), false},
		{"endpoint flag", []string{"-s3-endpoint", "http://x:9000"}, env(), true},
		{"endpoint env", nil, env("STRATA_S3_ENDPOINT", "http://x:9000"), true},
		{"explicitly off", []string{"-s3-endpoint", "http://x", "-s3-path-style=false"}, env(), false},
		{"explicitly off by env", nil, env("STRATA_S3_ENDPOINT", "http://x", "STRATA_S3_PATH_STYLE", "false"), false},
	}
	for _, c := range cases {
		cfg, err := ParseConfig(c.args, c.e)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if cfg.S3.PathStyle != c.want {
			t.Errorf("%s: PathStyle = %v, want %v", c.name, cfg.S3.PathStyle, c.want)
		}
	}
}

func TestNoCompactFlag(t *testing.T) {
	c, err := ParseConfig([]string{"-no-compact"}, env())
	if err != nil || c.CompactEnabled {
		t.Fatalf("%v %v", c.CompactEnabled, err)
	}
}

func TestInvalidInputIsRejectedWithAClearMessage(t *testing.T) {
	cases := map[string]struct {
		args []string
		e    func(string) string
		want string
	}{
		"bad duration in env":   {nil, env("STRATA_BUFFER_AGE", "5sec"), "STRATA_BUFFER_AGE"},
		"bad number in env":     {nil, env("STRATA_BUFFER_BYTES", "lots"), "STRATA_BUFFER_BYTES"},
		"bad bool in env":       {nil, env("STRATA_NO_COMPACT", "maybe"), "STRATA_NO_COMPACT"},
		"unknown flag":          {[]string{"-nope"}, env(), "nope"},
		"stray argument":        {[]string{"serve"}, env(), "unexpected arguments"},
		"key without secret":    {[]string{"-s3-access-key", "k"}, env(), "together"},
		"zero buffer":           {[]string{"-buffer-bytes", "0"}, env(), "buffer"},
		"empty bucket":          {[]string{"-s3-bucket", ""}, env(), "bucket"},
		"target below small":    {[]string{"-compact-small-bytes", "100", "-compact-target-bytes", "10"}, env(), "compact-target-bytes"},
		"unknown log level":     {[]string{"-log-level", "loud"}, env(), "log-level"},
		"negative orphan grace": {[]string{"-orphan-grace", "-1s"}, env(), "orphan-grace"},
	}
	for name, c := range cases {
		_, err := ParseConfig(c.args, c.e)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error mentioning %q, got %v", name, c.want, err)
		}
	}
}

func TestHelpShowsEveryEnvironmentVariable(t *testing.T) {
	_, err := ParseConfig([]string{"-h"}, env())
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("want flag.ErrHelp, got %v", err)
	}
	for _, name := range []string{"STRATA_LISTEN", "STRATA_MANIFEST", "STRATA_S3_ENDPOINT", "STRATA_S3_BUCKET", "STRATA_S3_ACCESS_KEY", "STRATA_ORPHAN_GRACE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("usage text does not mention %s", name)
		}
	}
}

// Secrets must not appear in the usage text or in an error message.
func TestSecretsAreNotLeaked(t *testing.T) {
	_, err := ParseConfig([]string{"-s3-secret-key", "TOPSECRET"}, env())
	if err == nil {
		t.Fatal("expected an error: secret without key")
	}
	if strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("error message leaks the secret: %v", err)
	}
	_, err = ParseConfig([]string{"-h"}, env("STRATA_S3_SECRET_KEY", "TOPSECRET", "STRATA_S3_ACCESS_KEY", "k"))
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("usage text leaks the secret: %v", err)
	}
}
