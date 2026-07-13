package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/models"
)

func TestGenerateAccountsHonorsContextWhileAnotherBatchRuns(t *testing.T) {
	s := &Scheduler{generationGate: make(chan struct{}, 1)}
	s.generationGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	inserted, err := s.GenerateAccounts(ctx, 1)
	if inserted != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("GenerateAccounts() = %d, %v; want 0, context canceled", inserted, err)
	}
}

func TestRequiredAccountRegistrationsIncludesUniqueIPGap(t *testing.T) {
	accounts := []*models.Account{
		healthyAccount(1, "198.51.100.1"),
		healthyAccount(2, "198.51.100.1"),
		healthyAccount(3, "198.51.100.2"),
		healthyAccount(4, "198.51.100.2"),
		healthyAccount(5, "198.51.100.3"),
		healthyAccount(6, "198.51.100.3"),
		healthyAccount(7, "198.51.100.3"),
		healthyAccount(8, "198.51.100.3"),
		healthyAccount(9, "198.51.100.3"),
		healthyAccount(10, "198.51.100.3"),
	}

	if got := requiredAccountRegistrations(5, 8, accounts); got != 2 {
		t.Fatalf("required registrations = %d, want 2 unique IPs", got)
	}
}

func TestRequiredAccountRegistrationsIncludesPoolGap(t *testing.T) {
	accounts := []*models.Account{
		healthyAccount(1, "198.51.100.1"),
		healthyAccount(2, "198.51.100.2"),
		healthyAccount(3, "198.51.100.3"),
		healthyAccount(4, "198.51.100.4"),
		healthyAccount(5, "198.51.100.5"),
		healthyAccount(6, "198.51.100.6"),
	}

	if got := requiredAccountRegistrations(5, 8, accounts); got != 2 {
		t.Fatalf("required registrations = %d, want 2 reserve accounts", got)
	}
}

func TestCurrentBindingConflicts(t *testing.T) {
	account := healthyAccount(7, "198.51.100.7")
	slot := &models.ProxySlot{PinnedPublicIP: "198.51.100.7"}

	if !currentBindingConflicts(account, slot, map[int64]bool{7: true}, nil) {
		t.Fatal("expected duplicate account binding to conflict")
	}
	if !currentBindingConflicts(account, slot, nil, map[string]bool{"198.51.100.7": true}) {
		t.Fatal("expected duplicate IP binding to conflict")
	}
	if currentBindingConflicts(account, slot, nil, nil) {
		t.Fatal("unique binding should not conflict")
	}
}

func healthyAccount(id int64, ip string) *models.Account {
	now := time.Now()
	return &models.Account{
		ID:             id,
		Status:         "active",
		LastPublicIP:   ip,
		LastLatencyMs:  100,
		LastSpeedBps:   1024 * 1024,
		LastPacketLoss: 0,
		LastTestedAt:   &now,
	}
}
