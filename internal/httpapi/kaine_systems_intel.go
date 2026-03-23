package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/edin-space/edin-backend/internal/memgraph"
	"github.com/edin-space/edin-backend/internal/store"
)

// SystemIntelResponse represents the combined system intelligence data.
type SystemIntelResponse struct {
	SystemName string `json:"system_name"`
	Hours      int    `json:"hours"` // 0 = all time

	// Current state from Memgraph
	CurrentState *SystemIntelCurrentState `json:"current_state,omitempty"`

	// Historical data from TimescaleDB EDDN feed
	EventStats       *store.EventStats         `json:"event_stats,omitempty"`
	TrafficTimeline  []store.TrafficBucket     `json:"traffic_timeline,omitempty"`
	CarrierActivity  []store.CarrierEvent      `json:"carrier_activity,omitempty"`
	Activity         []store.ActivityBucket    `json:"activity,omitempty"`
	MarketHistory    []store.MarketUpdate      `json:"market_history,omitempty"`
	CommodityHistory *store.CommodityHistory   `json:"commodity_history,omitempty"`
	RecentEvents     *store.EventsPage         `json:"recent_events,omitempty"`
	SoftwareStats    []store.SoftwareStats     `json:"software_stats,omitempty"`
	CarriersInSystem []FleetCarrierIntelData   `json:"carriers_in_system,omitempty"`

	// Deprecated: use EventStats instead
	TrafficStats *store.EventStats `json:"traffic_stats,omitempty"`
}

// SystemIntelCurrentState represents the current system state from Memgraph.
type SystemIntelCurrentState struct {
	Name                    string   `json:"name"`
	ControllingPower        string   `json:"controlling_power,omitempty"`
	Powers                  []string `json:"powers,omitempty"`
	PowerplayState          string   `json:"powerplay_state,omitempty"`
	Reinforcement           int64    `json:"reinforcement"`
	Undermining             int64    `json:"undermining"`
	ControlProgress         *float64 `json:"control_progress,omitempty"`
	Allegiance              string   `json:"allegiance,omitempty"`
	Government              string   `json:"government,omitempty"`
	Security                string   `json:"security,omitempty"`
	Population              int64    `json:"population,omitempty"`
	Economy                 string   `json:"economy,omitempty"`
	ControllingFaction      string   `json:"controlling_faction,omitempty"`
	ControllingFactionState string   `json:"controlling_faction_state,omitempty"`
	LastEDDNUpdate          string   `json:"last_eddn_update,omitempty"`

	// Conflict progress for expansion/contested systems
	ConflictProgress []map[string]any `json:"conflict_progress,omitempty"`

	// Counts
	StationCount int `json:"station_count,omitempty"`
	BodyCount    int `json:"body_count,omitempty"`
	FactionCount int `json:"faction_count,omitempty"`
	CarrierCount int `json:"carrier_count,omitempty"`

	// Detailed data
	Factions []memgraph.FactionPresence `json:"factions,omitempty"`
	Stations []StationIntelData         `json:"stations,omitempty"`
}

// StationIntelData represents a station for inline display within faction data.
type StationIntelData struct {
	Name               string `json:"name"`
	Type               string `json:"type,omitempty"`
	ControllingFaction string `json:"controlling_faction,omitempty"`
}

// FleetCarrierIntelData represents a fleet carrier for intel display.
type FleetCarrierIntelData struct {
	CarrierID string `json:"carrier_id"`
	Name      string `json:"name,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

// handleSystemIntel handles GET /api/kaine/systems/intel/{systemName}
// Returns combined system intelligence data from Memgraph and TimescaleDB.
//
// Query params:
//   - hours: time range in hours (default 24, 0 = all time)
//   - include: comma-separated sections (overview,traffic,carriers,activity,market,events,software)
//   - limit: max events to return for pagination (default 50, max 500)
//   - offset: pagination offset for events
func (s *Server) handleSystemIntel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	// Extract system name from path: /api/kaine/systems/intel/{systemName}
	path := strings.TrimPrefix(r.URL.Path, "/api/kaine/systems/intel/")
	systemName := strings.TrimSuffix(path, "/")

	if systemName == "" || systemName == "ws" {
		s.writeError(w, http.StatusBadRequest, "system name required")
		return
	}

	// URL decode the system name
	decodedName, err := url.QueryUnescape(systemName)
	if err != nil {
		decodedName = systemName
	}

	// Parse query parameters
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if parsed, err := strconv.Atoi(h); err == nil {
			hours = parsed
			// Allow 0 for all time, cap positive values at 8760 (1 year)
			if hours > 8760 {
				hours = 8760
			}
		}
	}

	// Parse pagination params for events
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
			if limit > 500 {
				limit = 500
			}
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// Parse include parameter (comma-separated list of sections to include)
	includeAll := true
	includeMap := make(map[string]bool)
	if inc := r.URL.Query().Get("include"); inc != "" {
		includeAll = false
		for _, section := range strings.Split(inc, ",") {
			includeMap[strings.TrimSpace(section)] = true
		}
	}

	response := SystemIntelResponse{
		SystemName: decodedName,
		Hours:      hours,
	}

	// Get current state from Memgraph
	if includeAll || includeMap["overview"] || includeMap["current"] {
		if s.memgraph != nil {
			systemFull, err := s.memgraph.GetSystemFull(r.Context(), decodedName)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("memgraph system lookup failed: %v", err))
			} else if systemFull != nil && systemFull.System != nil {
				sys := systemFull.System
				response.CurrentState = &SystemIntelCurrentState{
					Name:                    sys.Name,
					ControllingPower:        sys.ControllingPower,
					Powers:                  sys.Powers,
					PowerplayState:          sys.PowerplayState,
					Reinforcement:           sys.Reinforcement,
					Undermining:             sys.Undermining,
					ControlProgress:         sys.ControlProgress,
					Allegiance:              sys.Allegiance,
					Government:              sys.Government,
					Security:                sys.Security,
					Population:              sys.Population,
					Economy:                 sys.Economy,
					ControllingFaction:      sys.ControllingFaction,
					ControllingFactionState: sys.ControllingFactionState,
					StationCount:            len(systemFull.Stations),
					BodyCount:               len(systemFull.Bodies),
					FactionCount:            len(systemFull.Factions),
					CarrierCount:            len(systemFull.FleetCarriers),
				}
				if len(sys.PowerplayConflictProgress) > 0 {
					response.CurrentState.ConflictProgress = sys.PowerplayConflictProgress
				}
				if !sys.LastEDDNUpdate.IsZero() {
					response.CurrentState.LastEDDNUpdate = sys.LastEDDNUpdate.Format("2006-01-02T15:04:05Z")
				}

				// Include factions
				if len(systemFull.Factions) > 0 {
					response.CurrentState.Factions = systemFull.Factions
				}

				// Include stations (lightweight, excluding fleet carriers)
				if len(systemFull.Stations) > 0 {
					stations := make([]StationIntelData, 0, len(systemFull.Stations))
					for _, st := range systemFull.Stations {
						if st.Type == "Fleetcarrier" || st.Type == "Drake-Class Carrier" {
							continue
						}
						stations = append(stations, StationIntelData{
							Name:               st.Name,
							Type:               st.Type,
							ControllingFaction: st.ControllingFaction,
						})
					}
					if len(stations) > 0 {
						response.CurrentState.Stations = stations
					}
				}

				// Include carriers in system
				if len(systemFull.FleetCarriers) > 0 && (includeAll || includeMap["carriers"]) {
					for _, fc := range systemFull.FleetCarriers {
						carrier := FleetCarrierIntelData{
							CarrierID: fc.CarrierID,
							Name:      fc.Name,
						}
						if !fc.LastSeen.IsZero() {
							carrier.LastSeen = fc.LastSeen.Format("2006-01-02T15:04:05Z")
						}
						response.CarriersInSystem = append(response.CarriersInSystem, carrier)
					}
				}
			}
		}
	}

	// Get historical data from TimescaleDB EDDN feed
	if s.eddnIntelStore != nil {
		// Event stats (replaces traffic stats)
		if includeAll || includeMap["traffic"] || includeMap["stats"] {
			stats, err := s.eddnIntelStore.GetEventStats(r.Context(), decodedName, hours)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("event stats query failed: %v", err))
			} else {
				response.EventStats = stats
				response.TrafficStats = stats // backwards compat
			}

			// Traffic timeline
			timeline, err := s.eddnIntelStore.GetTrafficTimeline(r.Context(), decodedName, hours)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("traffic timeline query failed: %v", err))
			} else {
				response.TrafficTimeline = timeline
			}
		}

		// Fleet carrier activity
		if includeAll || includeMap["carriers"] {
			carriers, err := s.eddnIntelStore.GetFleetCarrierActivity(r.Context(), decodedName, hours)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("carrier activity query failed: %v", err))
			} else {
				response.CarrierActivity = carriers
			}
		}

		// Commander activity heatmap
		if includeAll || includeMap["activity"] {
			days := hours / 24
			if hours == 0 {
				days = 30 // Default to 30 days for all-time
			} else if days < 1 {
				days = 1
			} else if days > 30 {
				days = 30
			}
			activity, err := s.eddnIntelStore.GetCommanderActivity(r.Context(), decodedName, days)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("activity query failed: %v", err))
			} else {
				response.Activity = activity
			}
		}

		// Market history
		if includeAll || includeMap["market"] {
			market, err := s.eddnIntelStore.GetMarketHistory(r.Context(), decodedName, hours)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("market history query failed: %v", err))
			} else {
				response.MarketHistory = market
			}

			// Commodity history for charting
			commodities, err := s.eddnIntelStore.GetCommodityHistory(r.Context(), decodedName, hours)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("commodity history query failed: %v", err))
			} else {
				response.CommodityHistory = commodities
			}
		}

		// Recent events with pagination
		if includeAll || includeMap["events"] {
			events, err := s.eddnIntelStore.GetRecentEvents(r.Context(), decodedName, hours, limit, offset)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("recent events query failed: %v", err))
			} else {
				response.RecentEvents = events
			}
		}

		// Software stats
		if includeAll || includeMap["software"] {
			software, err := s.eddnIntelStore.GetSoftwareStats(r.Context(), decodedName, hours)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("software stats query failed: %v", err))
			} else {
				response.SoftwareStats = software
			}
		}
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleEventDetail handles GET /api/kaine/events/{eventID}
// Returns full event details including message data for modal display.
func (s *Server) handleEventDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	// Extract event ID from path: /api/kaine/events/{eventID}
	path := strings.TrimPrefix(r.URL.Path, "/api/kaine/events/")
	eventIDStr := strings.TrimSuffix(path, "/")

	if eventIDStr == "" {
		s.writeError(w, http.StatusBadRequest, "event ID required")
		return
	}

	eventID, err := strconv.ParseInt(eventIDStr, 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid event ID")
		return
	}

	if s.eddnIntelStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "EDDN store not available")
		return
	}

	event, err := s.eddnIntelStore.GetEventByID(r.Context(), eventID)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("get event by id failed: %v", err))
		s.writeError(w, http.StatusNotFound, "event not found")
		return
	}

	s.writeJSON(w, http.StatusOK, event)
}
