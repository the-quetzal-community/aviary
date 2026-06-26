package sentry

// dsn is the Sentry DSN, baked in at build time by inject_dsn.sh (which generates a gitignored
// dsn_injected.go that sets it). Empty by default, so a build with no SENTRY_DSN still compiles
// and runs with Sentry disabled — and the DSN never lives in git.
var dsn string

// version is the release version baked in at build time (from deploy.sh's velopack version, via
// inject_dsn.sh / $AVIARY_VERSION). Used for the Sentry release tag — the project's
// application/config/version setting is unreliable (often stale). Empty ⇒ fall back to it.
var version string
