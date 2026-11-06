package vault

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"3d-vault/internal/rules"
)

func TestRequestUnlock(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	e.svc.now = func() time.Time { return now }
	asset := e.newAsset("modelos/camion.blend").Asset

	wantCode := func(err error, code rules.Code) {
		t.Helper()
		var ruleErr *rules.Error
		if !errors.As(err, &ruleErr) || ruleErr.Code != code {
			t.Fatalf("error = %v, quiero %s", err, code)
		}
	}

	// Libre: no hay a quién pedírselo.
	wantCode(e.svc.RequestUnlock(e.ctx, e.ana, asset.ID), rules.CodeInvalidInput)

	e.mustLock(e.juan, asset.ID)
	// El bloqueo es de Juan, aunque sea en otra PC suya.
	wantCode(e.svc.RequestUnlock(e.ctx, e.juanPC2, asset.ID), rules.CodeInvalidInput)

	seq, err := e.svc.CurrentSeq(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RequestUnlock(e.ctx, e.ana, asset.ID); err != nil {
		t.Fatalf("RequestUnlock: %v", err)
	}
	page, err := e.svc.EventsSince(e.ctx, []int64{e.project.ID}, seq)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != EventUnlockRequested {
		t.Fatalf("eventos = %+v, %v", page.Events, err)
	}
	var payload eventPayload
	if err := json.Unmarshal(page.Events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ActorName != "Ana" || payload.Lock == nil || payload.Lock.UserID != e.juan.UserID || payload.Path != "modelos/camion.blend" {
		t.Fatalf("payload = %+v", payload)
	}
	if got := e.activityCount(actionRequestUnlock); got != 1 {
		t.Fatalf("actividad = %d", got)
	}

	// Repetir enseguida se rechaza; pasados 5 minutos, vale.
	wantCode(e.svc.RequestUnlock(e.ctx, e.ana, asset.ID), rules.CodeInvalidInput)
	now = now.Add(requestEvery + time.Second)
	if err := e.svc.RequestUnlock(e.ctx, e.ana, asset.ID); err != nil {
		t.Fatalf("RequestUnlock tras esperar: %v", err)
	}
}
