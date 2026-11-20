package main

import (
	"lanflow/internal/app"
	"lanflow/internal/view/settings"
)

// newService wires the real service (internal/app). When it cannot be
// created main tells the user and exits (backend in main.go). lang is the
// language of a first run ("zh" or "en").
func newService(lang string) (app.API, error) {
	o := app.DefaultOptions("Lanflow")
	o.Language = lang
	o.Version = settings.AppVersion()
	o.CaptureCrashes = true
	svc, err := app.NewService(o)
	if err != nil {
		return nil, err
	}
	return svc, nil
}
