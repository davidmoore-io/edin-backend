package httpapi

import (
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/galaxystore"
)

type modalFaction struct {
	FactionName   string    `json:"faction_name"`
	SystemName    string    `json:"system_name"`
	Influence     float64   `json:"influence"`
	State         string    `json:"state,omitempty"`
	ActiveStates  []string  `json:"active_states,omitempty"`
	PendingStates []string  `json:"pending_states,omitempty"`
	Happiness     string    `json:"happiness,omitempty"`
	LastEventTime time.Time `json:"last_event_time,omitempty"`
}

type modalStation struct {
	ID64               int64     `json:"id64"`
	Name               string    `json:"name"`
	Type               string    `json:"type,omitempty"`
	SystemName         string    `json:"system_name,omitempty"`
	DistanceLS         float64   `json:"distance_ls,omitempty"`
	MaxPad             string    `json:"max_pad,omitempty"`
	IsPlanetary        bool      `json:"is_planetary,omitempty"`
	Services           []string  `json:"services,omitempty"`
	ControllingFaction string    `json:"controlling_faction,omitempty"`
	HasMarket          bool      `json:"has_market,omitempty"`
	HasShipyard        bool      `json:"has_shipyard,omitempty"`
	HasOutfitting      bool      `json:"has_outfitting,omitempty"`
	LastEDDNUpdate     time.Time `json:"last_eddn_update,omitempty"`
}

func modalFactions(rows []galaxystore.FactionPresence) []modalFaction {
	out := make([]modalFaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, modalFaction{
			FactionName:   row.FactionName,
			SystemName:    row.SystemName,
			Influence:     row.Influence,
			State:         row.State,
			ActiveStates:  row.ActiveStates,
			PendingStates: row.PendingStates,
			Happiness:     row.Happiness,
			LastEventTime: row.LastEventTime,
		})
	}
	return out
}

func modalStations(rows []galaxystore.StationData) []modalStation {
	out := make([]modalStation, 0, len(rows))
	for _, row := range rows {
		if strings.EqualFold(row.Type, "FleetCarrier") {
			continue
		}
		out = append(out, modalStation{
			ID64:               row.ID64,
			Name:               row.Name,
			Type:               row.Type,
			SystemName:         row.SystemName,
			DistanceLS:         row.DistanceLS,
			MaxPad:             row.MaxPad,
			IsPlanetary:        row.IsPlanetary,
			Services:           row.Services,
			ControllingFaction: row.ControllingFaction,
			HasMarket:          row.HasMarket,
			HasShipyard:        row.HasShipyard,
			HasOutfitting:      row.HasOutfitting,
			LastEDDNUpdate:     row.LastEDDNUpdate,
		})
	}
	return out
}
