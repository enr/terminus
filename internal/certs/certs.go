// Package certs holds what the modules that look at TLS certificates share.
package certs

import (
	"time"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// ShortLived are certificates graded on the share of their lifetime left instead of days: the
// Caddy internal CA (12 hours) and the short-lived ACME certificates (6 days).
const ShortLived = 30 * 24 * time.Hour

// GradeExpiry grades the expiry of a certificate on t (days left, lower is worse). ACME clients
// renew when a third of the lifetime is left: for short-lived certificates less than a sixth left
// means the renewal is late.
func GradeExpiry(notBefore, notAfter time.Time, daysLeft float64, t module.Threshold) model.Severity {
	lifetime := notAfter.Sub(notBefore)
	if notBefore.IsZero() || lifetime <= 0 || lifetime >= ShortLived {
		return t.Grade(daysLeft)
	}
	switch left := daysLeft * 24 * float64(time.Hour) / float64(lifetime); {
	case daysLeft <= 0:
		return model.SeverityFail
	case left < 1.0/6:
		return model.SeverityWarn
	default:
		return model.SeverityOK
	}
}
