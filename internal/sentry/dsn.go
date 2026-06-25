package sentry

// dsn is the Sentry DSN, baked in at build time by inject_dsn.sh (which generates a gitignored
// dsn_injected.go that sets it). Empty by default, so a build with no SENTRY_DSN still compiles
// and runs with Sentry disabled — and the DSN never lives in git.
var dsn string
