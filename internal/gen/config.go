package gen

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config controls a run.
type Config struct {
	Addr     string
	Profile  string
	Schedule Schedule
	// Sources is the number of parallel streams (think: separate services or
	// hosts shipping logs). The total rate is split between them.
	Sources int
	// LateFraction of lines get a timestamp up to LateMax in the past, the way
	// logs from a slow or buffering host arrive after newer ones. Late lines make
	// a segment's time range wider, which weakens time-based skipping.
	LateFraction float64
	LateMax      time.Duration
	// Duration of 0 means run until cancelled.
	Duration time.Duration
	Batch    int           // most lines in one request
	Tick     time.Duration // how often the schedule is checked
	Seed     uint64        // content is reproducible for a seed (the run ID is not)
	Report   time.Duration // progress line interval

	// Verify checks, when the run ends, that every line the server acknowledged
	// can be found. VerifySettle is the pause that lets the server seal its last
	// buffer first (default server: 5 seconds).
	Verify        bool
	VerifySettle  time.Duration
	VerifyWorkers int
	FailOnLoss    bool

	ConnectTimeout time.Duration
	// Grace is how long, after a stop, an in-flight batch may keep retrying so
	// that it is not left half delivered.
	Grace  time.Duration
	Logger *slog.Logger
}

// Profile is a named set of defaults.
type Profile struct {
	Name         string
	Description  string
	Schedule     Schedule
	Sources      int
	LateFraction float64
	LateMax      time.Duration
}

// Profiles are the built-in workloads. Every field can still be overridden with
// a flag. None of them claims to be "what production looks like": they are
// named points to test at.
var Profiles = []Profile{
	{Name: "demo", Description: "a quiet service: 20 lines/s, one stream, no bursts (about 0.4 GB/day stored)",
		Schedule: Schedule{Rate: 20, BurstFactor: 1}, Sources: 1},
	{Name: "busy", Description: "a busy service: 1,000 lines/s over 2 streams, 3x bursts of 5s every minute, 2% late lines (about 19 GB/day stored)",
		Schedule: Schedule{Rate: 1000, BurstFactor: 3, BurstEvery: time.Minute, BurstFor: 5 * time.Second}, Sources: 2,
		LateFraction: 0.02, LateMax: 2 * time.Minute},
	{Name: "peak", Description: "a heavy day: 5,000 lines/s over 4 streams, 3x bursts of 5s every 30s, 5% late lines (about 97 GB/day stored)",
		Schedule: Schedule{Rate: 5000, BurstFactor: 3, BurstEvery: 30 * time.Second, BurstFor: 5 * time.Second}, Sources: 4,
		LateFraction: 0.05, LateMax: 5 * time.Minute},
}

func profileByName(name string) (Profile, bool) {
	for _, p := range Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

func profileNames() string {
	var names []string
	for _, p := range Profiles {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ParseConfig reads flags, with STRATA_GEN_* environment variables as defaults
// for the main ones (so a container can be configured without a command line).
// A profile sets the defaults for the workload; any flag or variable that is
// given explicitly overrides the profile.
func ParseConfig(args []string, getenv func(string) string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("strata-gen", flag.ContinueOnError)
	var usage bytes.Buffer
	fs.SetOutput(&usage)

	var envErrs []string
	envSet := map[string]bool{}
	str := func(p *string, name, env, def, help string) {
		if v := getenv(env); v != "" {
			def, envSet[name] = v, true
		}
		fs.StringVar(p, name, def, help+" [env "+env+"]")
	}
	flt := func(p *float64, name, env string, help string) {
		def := 0.0
		if v := getenv(env); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				envErrs = append(envErrs, fmt.Sprintf("%s=%q: %v", env, v, err))
			} else {
				def, envSet[name] = f, true
			}
		}
		fs.Float64Var(p, name, def, help+" [env "+env+"]")
	}
	integer := func(p *int, name, env string, help string) {
		def := 0
		if v := getenv(env); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				envErrs = append(envErrs, fmt.Sprintf("%s=%q: %v", env, v, err))
			} else {
				def, envSet[name] = n, true
			}
		}
		fs.IntVar(p, name, def, help+" [env "+env+"]")
	}
	dur := func(p *time.Duration, name, env string, def time.Duration, help string) {
		if v := getenv(env); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				envErrs = append(envErrs, fmt.Sprintf("%s=%q: %v", env, v, err))
			} else {
				def, envSet[name] = d, true
			}
		}
		fs.DurationVar(p, name, def, help+" [env "+env+"]")
	}

	var profileName string
	var seed int64
	var rate, burstFactor, lateFraction float64
	var sources int
	var burstEvery, burstFor, lateMax time.Duration

	str(&c.Addr, "addr", "STRATA_GEN_ADDR", "127.0.0.1:7070", "Strata server address")
	str(&profileName, "profile", "STRATA_GEN_PROFILE", "demo", "workload preset: "+profileNames())
	flt(&rate, "rate", "STRATA_GEN_RATE", "lines per second, all streams together (overrides the profile)")
	integer(&sources, "sources", "STRATA_GEN_SOURCES", "parallel streams (overrides the profile)")
	flt(&burstFactor, "burst-factor", "STRATA_GEN_BURST_FACTOR", "rate multiplier during a burst; 1 means no bursts (overrides the profile)")
	dur(&burstEvery, "burst-every", "STRATA_GEN_BURST_EVERY", 0, "a burst ends at every multiple of this (overrides the profile)")
	dur(&burstFor, "burst-for", "STRATA_GEN_BURST_FOR", 0, "how long a burst lasts (overrides the profile)")
	flt(&lateFraction, "late-fraction", "STRATA_GEN_LATE_FRACTION", "fraction of lines stamped in the past, 0 to 1 (overrides the profile)")
	dur(&lateMax, "late-max", "STRATA_GEN_LATE_MAX", 0, "how far in the past a late line can be (overrides the profile)")
	dur(&c.Duration, "duration", "STRATA_GEN_DURATION", 0, "stop after this long; 0 runs until interrupted")
	integer(&c.Batch, "batch", "STRATA_GEN_BATCH", "most lines per request (default 200)")
	var seedStr string
	str(&seedStr, "seed", "STRATA_GEN_SEED", "1", "random seed: the same seed produces the same lines")
	dur(&c.Report, "report", "STRATA_GEN_REPORT", 10*time.Second, "progress line interval")
	fs.BoolVar(&c.Verify, "verify", false, "when the run ends, check that every acknowledged line is stored (on by default for a run with a -duration)")
	noVerify := fs.Bool("no-verify", false, "never verify, even for a run with a -duration")
	dur(&c.VerifySettle, "verify-settle", "STRATA_GEN_VERIFY_SETTLE", 12*time.Second, "pause before verifying, so the server can seal its last buffer")
	fs.IntVar(&c.VerifyWorkers, "verify-workers", 4, "parallel search requests while verifying")
	fs.BoolVar(&c.FailOnLoss, "fail-on-loss", false, "exit with status 2 if verification finds lost lines")
	dur(&c.ConnectTimeout, "connect-timeout", "STRATA_GEN_CONNECT_TIMEOUT", 60*time.Second, "give up if the server cannot be reached for this long")

	if len(envErrs) > 0 {
		return c, errors.New("invalid environment: " + strings.Join(envErrs, "; "))
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return c, &HelpRequested{Usage: usage.String() + profileHelp()}
		}
		return c, fmt.Errorf("%w\n%s", err, usage.String())
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	prof, ok := profileByName(profileName)
	if !ok {
		return c, fmt.Errorf("unknown profile %q (choose one of: %s)", profileName, profileNames())
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	for k := range envSet {
		given[k] = true
	}

	c.Profile = prof.Name
	c.Schedule = prof.Schedule
	c.Sources, c.LateFraction, c.LateMax = prof.Sources, prof.LateFraction, prof.LateMax
	if given["rate"] {
		c.Schedule.Rate = rate
	}
	if given["sources"] {
		c.Sources = sources
	}
	if given["burst-factor"] {
		c.Schedule.BurstFactor = burstFactor
	}
	if given["burst-every"] {
		c.Schedule.BurstEvery = burstEvery
	}
	if given["burst-for"] {
		c.Schedule.BurstFor = burstFor
	}
	if given["late-fraction"] {
		c.LateFraction = lateFraction
	}
	if given["late-max"] {
		c.LateMax = lateMax
	}
	n, err := strconv.ParseInt(seedStr, 10, 64)
	if err != nil {
		return c, fmt.Errorf("seed %q is not a whole number", seedStr)
	}
	seed = n
	c.Seed = uint64(seed)

	if c.Batch == 0 {
		c.Batch = 200
	}
	c.Tick = 100 * time.Millisecond
	c.Grace = 30 * time.Second
	if c.Duration > 0 && !given["verify"] {
		c.Verify = true // a finite run is a test: check it
	}
	if *noVerify {
		c.Verify = false
	}
	return c, c.Validate()
}

// Validate rejects configurations that cannot work.
func (c Config) Validate() error {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if err := c.Schedule.Validate(); err != nil {
		bad("%v", err)
	}
	if c.Addr == "" {
		bad("server address is empty")
	}
	if c.Sources < 1 || c.Sources > 64 {
		bad("sources must be between 1 and 64, got %d", c.Sources)
	}
	if c.Batch < 1 || c.Batch > 10000 {
		bad("batch must be between 1 and 10000, got %d", c.Batch)
	}
	if c.LateFraction < 0 || c.LateFraction > 1 {
		bad("late-fraction must be between 0 and 1, got %v", c.LateFraction)
	}
	if c.LateFraction > 0 && c.LateMax <= 0 {
		bad("late lines need a late-max")
	}
	if c.Duration < 0 {
		bad("duration must not be negative")
	}
	if c.Verify && c.Duration == 0 {
		bad("-verify needs a -duration: an endless run has no end to verify")
	}
	if c.Report <= 0 {
		bad("report interval must be positive")
	}
	if c.VerifyWorkers < 1 {
		bad("verify-workers must be at least 1")
	}
	if len(problems) > 0 {
		return errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return nil
}

// HelpRequested is returned for -h: print Usage and exit successfully.
type HelpRequested struct{ Usage string }

func (h *HelpRequested) Error() string { return h.Usage }
func (h *HelpRequested) Unwrap() error { return flag.ErrHelp }

func profileHelp() string {
	var b strings.Builder
	b.WriteString("\nprofiles:\n")
	for _, p := range Profiles {
		fmt.Fprintf(&b, "  %-5s %s\n", p.Name, p.Description)
	}
	return b.String()
}
