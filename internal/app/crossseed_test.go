package app

import (
	"context"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
)

// switchingDaemon simule un démon cross-seed qui s'arrête puis redémarre ; probes compte les sondes TCP.
type switchingDaemon struct {
	reachable bool
	probes    int
}

func (daemon *switchingDaemon) probe(context.Context, string) bool {
	daemon.probes++
	return daemon.reachable
}

func TestCrossSeedResolver_followsTheDaemonAndCachesForThirtySeconds(t *testing.T) {
	daemon := &switchingDaemon{reachable: true}
	clock := fixedNow
	settings := config.Config{CrossSeedMode: crossseed.ModeAuto, CrossSeed: &config.CrossSeedDaemon{BaseURL: "http://127.0.0.1:2468"}}
	resolver := newCrossSeedResolver(settings, nil, daemon.probe, func() time.Time { return clock })
	ctx := context.Background()

	first := resolver.resolve(ctx)
	daemon.reachable = false
	cached := resolver.resolve(ctx)
	clock = clock.Add(crossSeedResolutionTTL + time.Second)
	refreshed := resolver.resolve(ctx)

	if first.Behavior != crossseed.Delegate || cached.Behavior != crossseed.Delegate || daemon.probes != 2 {
		t.Fatalf("gotValue %v then %v after %d probes, expectedValue delegate cached for 30 s", first.Behavior, cached.Behavior, daemon.probes)
	}
	if refreshed.Behavior != crossseed.Native || refreshed.Warning == "" {
		t.Fatalf("gotValue %+v, expectedValue native with a warning once the daemon stops", refreshed)
	}
}

func TestService_statusAndAcceptUseTheCurrentCrossSeedResolution(t *testing.T) {
	fixture := newDecisionFixture(t)
	resolutions := []CrossSeedResolution{{Behavior: crossseed.Native}, {Behavior: crossseed.Disabled, Warning: "daemon gone"}}
	calls := 0
	service := New(Deps{
		Store: fixture.store, Client: fixture.resumer, Now: func() time.Time { return fixedNow },
		ResolveCrossSeed: func(context.Context) CrossSeedResolution {
			resolution := resolutions[min(calls, len(resolutions)-1)]
			calls++
			return resolution
		},
	})
	ctx := context.Background()

	before, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if before.CrossSeedMode != "native" || after.CrossSeedMode != "disabled" || len(after.Warnings) != 1 {
		t.Fatalf("gotValue %+v then %+v, expectedValue the mode re-resolved at each call", before, after)
	}
	first, second := fixture.verifiedCandidate("aa"), fixture.verifiedCandidate("bb")
	firstID, secondID := fixture.proposal(first, "full"), fixture.proposal(second, "full")
	if err := service.Accept(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	if err := service.Accept(ctx, secondID); err == nil {
		t.Fatal("expectedValue the second torrent of the group refused once cross-seed is disabled")
	}
}
