package http

import (
	"hw-balance/pkg/health"
)

type HealthHandler = health.Handler

func NewHealthHandler(pinger health.Pinger) *HealthHandler {
	return health.NewHandler(pinger)
}
