package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

const rouletteDirName = "RUSSIAN_ROULETTE"
const rouletteStateFileName = "state.json"
const rouletteLoseImageName = "you_lose.jpg"
const rouletteDrumSize = 100
const defaultRouletteCooldown = 60 * time.Minute

type RouletteUserStats struct {
	Spins      int       `json:"spins"`
	Losses     int       `json:"losses"`
	Wins       int       `json:"wins"`
	LastSpinAt time.Time `json:"last_spin_at"`
	LastLossAt time.Time `json:"last_loss_at"`
}

type RouletteGlobalStats struct {
	TotalSpins  int       `json:"total_spins"`
	TotalLosses int       `json:"total_losses"`
	LastResetAt time.Time `json:"last_reset_at"`
}

type RouletteState struct {
	Version         int                           `json:"version"`
	DrumSize        int                           `json:"drum_size"`
	LosingPosition  int                           `json:"losing_position"`
	CurrentPosition int                           `json:"current_position"`
	CooldownSeconds int                           `json:"cooldown_seconds"`
	Global          RouletteGlobalStats           `json:"global"`
	Users           map[string]*RouletteUserStats `json:"users"`
}

var rouletteMu sync.Mutex

func getRouletteCooldown() time.Duration {
	if cfg.rouletteCooldown <= 0 {
		return defaultRouletteCooldown
	}
	return cfg.rouletteCooldown
}

func rouletteStatePath() string {
	return filepath.Join(cfg.photoPath, rouletteDirName, rouletteStateFileName)
}

func rouletteLoseImagePath() string {
	return filepath.Join(cfg.photoPath, rouletteDirName, rouletteLoseImageName)
}

func defaultRouletteState() *RouletteState {
	return &RouletteState{Version: 1, DrumSize: rouletteDrumSize, LosingPosition: rand.Intn(rouletteDrumSize), CurrentPosition: 0,
		CooldownSeconds: int(getRouletteCooldown().Seconds()), Users: map[string]*RouletteUserStats{}}
}

func normalizeRouletteState(state *RouletteState) {
	state.Version = 1
	state.DrumSize = rouletteDrumSize
	state.CooldownSeconds = int(getRouletteCooldown().Seconds())
	if state.Users == nil {
		state.Users = map[string]*RouletteUserStats{}
	}
	state.CurrentPosition = ((state.CurrentPosition % rouletteDrumSize) + rouletteDrumSize) % rouletteDrumSize
	state.LosingPosition = ((state.LosingPosition % rouletteDrumSize) + rouletteDrumSize) % rouletteDrumSize
}

func loadOrInitRouletteState() (*RouletteState, error) {
	statePath := rouletteStatePath()
	if err := os.MkdirAll(filepath.Dir(statePath), os.ModePerm); err != nil {
		return nil, err
	}
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		state := defaultRouletteState()
		if err := saveRouletteState(state); err != nil {
			return nil, err
		}
		return state, nil
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	var state RouletteState
	if err = json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	normalizeRouletteState(&state)
	return &state, nil
}

func saveRouletteState(state *RouletteState) error {
	normalizeRouletteState(state)
	statePath := rouletteStatePath()
	tmpPath := statePath + ".tmp"
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, statePath)
}

func formatCooldownLeft(d time.Duration) string {
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%d min %d sec", minutes, seconds)
}

func tryRussianRoulette(update *tgbotapi.Update) (bool, string, error) {
	if !cfg.russianRoulette {
		return false, "", nil
	}
	if update == nil || update.Message == nil || update.Message.Chat.IsPrivate() {
		return false, "", nil
	}

	userKey := strconv.FormatInt(update.Message.From.ID, 10)
	now := time.Now().UTC()

	rouletteMu.Lock()
	defer rouletteMu.Unlock()

	state, err := loadOrInitRouletteState()
	if err != nil {
		return false, "", err
	}

	userStats := state.Users[userKey]
	if userStats == nil {
		userStats = &RouletteUserStats{}
		state.Users[userKey] = userStats
	}
	if !userStats.LastSpinAt.IsZero() {
		nextAllowed := userStats.LastSpinAt.Add(getRouletteCooldown())
		if now.Before(nextAllowed) {
			left := nextAllowed.Sub(now)
			return true, "⏳ Roulette cooldown: " + formatCooldownLeft(left), nil
		}
	}

	state.CurrentPosition = (state.CurrentPosition + 1) % rouletteDrumSize
	state.Global.TotalSpins++
	userStats.Spins++
	userStats.LastSpinAt = now

	if state.CurrentPosition == state.LosingPosition {
		state.Global.TotalLosses++
		userStats.Losses++
		userStats.LastLossAt = now
		state.LosingPosition = rand.Intn(rouletteDrumSize)
		state.CurrentPosition = 0
	} else {
		userStats.Wins++
	}

	if err = saveRouletteState(state); err != nil {
		return false, "", err
	}

	if userStats.LastLossAt.Equal(now) {
		return false, rouletteLoseImagePath(), nil
	}
	return false, "", nil
}

func getUserRouletteStats(userID int64) (string, error) {
	rouletteMu.Lock()
	defer rouletteMu.Unlock()

	state, err := loadOrInitRouletteState()
	if err != nil {
		return "", err
	}
	userKey := strconv.FormatInt(userID, 10)
	userStats := state.Users[userKey]
	if userStats == nil {
		return "🎯 Your roulette stats:\nSpins: 0\nWins: 0\nLosses: 0", nil
	}

	cooldownText := "ready now"
	if !userStats.LastSpinAt.IsZero() {
		nextAllowed := userStats.LastSpinAt.Add(getRouletteCooldown())
		if time.Now().UTC().Before(nextAllowed) {
			cooldownText = formatCooldownLeft(nextAllowed.Sub(time.Now().UTC()))
		}
	}

	lastLoss := "never"
	if !userStats.LastLossAt.IsZero() {
		lastLoss = userStats.LastLossAt.Format(time.RFC3339)
	}

	return fmt.Sprintf("🎯 Your roulette stats:\nSpins: %d\nWins: %d\nLosses: %d\nCooldown: %s\nLast loss: %s",
		userStats.Spins, userStats.Wins, userStats.Losses, cooldownText, lastLoss), nil
}

func resetRoulette(adminID int64) (string, error) {
	rouletteMu.Lock()
	defer rouletteMu.Unlock()

	state, err := loadOrInitRouletteState()
	if err != nil {
		return "", err
	}
	state.LosingPosition = rand.Intn(rouletteDrumSize)
	state.CurrentPosition = 0
	state.Global.LastResetAt = time.Now().UTC()
	if err = saveRouletteState(state); err != nil {
		return "", err
	}
	return fmt.Sprintf("🔄 Roulette reset by admin %d. New losing position selected.", adminID), nil
}
