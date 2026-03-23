// Package main provides the galaxy-exporter command for generating static binary
// files for the galaxy visualization. This runs as a scheduled job (systemd timer)
// to periodically export the full galaxy data from Memgraph.
//
// Output files:
//   - positions.bin: Binary file with all system positions and IDs
//   - metadata.json: JSON file with power/allegiance indices for coloring
//   - manifest.json: Metadata about the export (count, timestamp)
package main

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/edin-space/edin-backend/internal/memgraph"
)

// BinaryHeader is the 16-byte header for positions.bin
// Magic: "GLXY" (4 bytes)
// Version: uint32 (4 bytes)
// Count: uint32 (4 bytes)
// Timestamp: uint32 (4 bytes) - Unix timestamp
const (
	Magic        = "GLXY"
	Version      = 1
	HeaderSize   = 16
	PositionSize = 12 // 3 × float32
	IDSize       = 8  // uint64
)

// PP2 epoch: Powerplay 2.0 cycle 1 started 24 October 2024 07:00 UTC.
// All cycle numbers are derived from this fixed point.
var PP2Epoch = time.Date(2024, 10, 24, 7, 0, 0, 0, time.UTC)

// pp2CycleNumber returns the PP2 cycle number for a given cycle start time.
func pp2CycleNumber(cycleStart time.Time) int {
	days := int(cycleStart.Sub(PP2Epoch).Hours() / 24)
	return (days / 7) + 1
}

// Metadata represents the JSON metadata file structure
type Metadata struct {
	GeneratedAt  time.Time          `json:"generated_at"`
	SystemCount  int                `json:"system_count"`
	Powers       map[string][]int   `json:"powers"`       // Power name -> array of system indices
	Allegiances  map[string][]int   `json:"allegiances"`  // Allegiance -> array of system indices
	States       map[string][]int   `json:"states"`       // Powerplay state -> array of system indices
}

// Manifest represents the manifest.json file
type Manifest struct {
	Version     int       `json:"version"`
	SystemCount int       `json:"system_count"`
	GeneratedAt time.Time `json:"generated_at"`
	Files       []string  `json:"files"`
}

// History represents the history.json file for the timeline scrubber
type History struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Cycles      []HistoryCycle `json:"cycles"`
}

// HistoryCycle holds per-power and per-state index arrays for one weekly cycle
type HistoryCycle struct {
	CycleStart time.Time        `json:"cycle_start"`
	Tick       int              `json:"tick"`
	Powers     map[string][]int `json:"powers"`
	States     map[string][]int `json:"states"`
}

func main() {
	outputDir := flag.String("output", "/srv/galaxy", "Output directory for generated files")
	memgraphHost := flag.String("memgraph-host", "10.8.0.3", "Memgraph host (VPN IP)")
	memgraphPort := flag.Int("memgraph-port", 7687, "Memgraph port")
	batchSize := flag.Int("batch-size", 50000, "Batch size for Memgraph queries")
	compress := flag.Bool("compress", true, "Gzip compress the binary file")
	eddnDSN := flag.String("eddn-dsn", "", "EDDN TimescaleDB DSN for history export (optional, skips history if empty)")
	historyCycles := flag.Int("history-cycles", 12, "Number of historical cycles to include in history.json")
	historyMinCycle := flag.Int("history-min-cycle", 72, "Earliest PP2 cycle number to include in history.json (filters out low-quality data)")
	flag.Parse()

	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Printf("Galaxy Exporter starting...")
	log.Printf("Output directory: %s", *outputDir)
	log.Printf("Memgraph: %s:%d", *memgraphHost, *memgraphPort)

	// Ensure output directory exists
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// Connect to Memgraph
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client, err := memgraph.NewClient(memgraph.Config{
		Host: *memgraphHost,
		Port: *memgraphPort,
	})
	if err != nil {
		log.Fatalf("Failed to create Memgraph client: %v", err)
	}
	defer client.Close(ctx)

	if err := client.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect to Memgraph: %v", err)
	}

	// Export data
	startTime := time.Now()
	count, metadata, ids, err := exportGalaxyData(ctx, client, *outputDir, *batchSize, *compress)
	if err != nil {
		log.Fatalf("Export failed: %v", err)
	}

	duration := time.Since(startTime)
	log.Printf("Export complete: %d systems in %v", count, duration)

	// Write metadata.json
	metadataPath := filepath.Join(*outputDir, "metadata.json")
	if err := writeMetadata(metadataPath, metadata); err != nil {
		log.Fatalf("Failed to write metadata: %v", err)
	}
	log.Printf("Wrote metadata.json (%d powers, %d allegiances)",
		len(metadata.Powers), len(metadata.Allegiances))

	// Export history.json if EDDN DSN is provided
	historyOK := false
	if *eddnDSN != "" {
		log.Printf("Exporting history.json from EDDN TimescaleDB...")
		historyPath := filepath.Join(*outputDir, "history.json")
		if err := exportHistory(ctx, *eddnDSN, historyPath, ids, *historyCycles, *historyMinCycle); err != nil {
			log.Printf("WARNING: Failed to export history: %v", err)
		} else {
			historyOK = true
			log.Printf("Wrote history.json")
		}
	}

	// Write manifest.json
	manifestPath := filepath.Join(*outputDir, "manifest.json")
	files := []string{"positions.bin", "metadata.json"}
	if *compress {
		files[0] = "positions.bin.gz"
	}
	if historyOK {
		files = append(files, "history.json")
	}
	if err := writeManifest(manifestPath, count, files); err != nil {
		log.Fatalf("Failed to write manifest: %v", err)
	}
	log.Printf("Wrote manifest.json")

	log.Printf("Galaxy export completed successfully")
}

// exportGalaxyData exports all systems to a binary file and collects metadata.
// Returns the system count, metadata, and the ordered slice of id64 values (for history mapping).
func exportGalaxyData(ctx context.Context, client *memgraph.Client, outputDir string, batchSize int, compress bool) (int, *Metadata, []uint64, error) {
	binPath := filepath.Join(outputDir, "positions.bin")
	if compress {
		binPath += ".gz"
	}

	// Create temporary file first, then rename for atomicity
	tmpPath := binPath + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("create file: %w", err)
	}
	defer os.Remove(tmpPath) // Clean up on error

	var writer *binaryWriter
	if compress {
		gzWriter := gzip.NewWriter(file)
		defer gzWriter.Close()
		writer = newBinaryWriter(gzWriter)
	} else {
		writer = newBinaryWriter(file)
	}

	// Write placeholder header (will update with count later)
	if err := writer.writeHeader(0); err != nil {
		file.Close()
		return 0, nil, nil, fmt.Errorf("write header: %w", err)
	}

	// Metadata collection
	metadata := &Metadata{
		GeneratedAt: time.Now().UTC(),
		Powers:      make(map[string][]int),
		Allegiances: make(map[string][]int),
		States:      make(map[string][]int),
	}

	systemIndex := 0
	var positions []positionData
	var ids []uint64

	// Stream systems from Memgraph
	log.Printf("Streaming systems from Memgraph (batch size: %d)...", batchSize)

	count, err := client.GetAllSystemsMinimal(ctx, batchSize, func(batch []memgraph.MinimalSystem) error {
		for _, sys := range batch {
			// Store position and ID
			positions = append(positions, positionData{
				X: float32(sys.X),
				Y: float32(sys.Y),
				Z: float32(sys.Z),
			})
			ids = append(ids, uint64(sys.ID64))

			// Collect metadata indices
			if sys.ControllingPower != "" {
				metadata.Powers[sys.ControllingPower] = append(
					metadata.Powers[sys.ControllingPower], systemIndex)
			}
			if sys.PowerplayState != "" {
				metadata.States[sys.PowerplayState] = append(
					metadata.States[sys.PowerplayState], systemIndex)
			}
			if sys.Allegiance != "" {
				metadata.Allegiances[sys.Allegiance] = append(
					metadata.Allegiances[sys.Allegiance], systemIndex)
			}

			systemIndex++
		}

		log.Printf("Processed %d systems...", systemIndex)
		return nil
	})

	if err != nil {
		file.Close()
		return 0, nil, nil, fmt.Errorf("stream systems: %w", err)
	}

	// Write positions
	for _, pos := range positions {
		if err := writer.writePosition(pos); err != nil {
			file.Close()
			return 0, nil, nil, fmt.Errorf("write position: %w", err)
		}
	}

	// Write IDs
	for _, id := range ids {
		if err := writer.writeID(id); err != nil {
			file.Close()
			return 0, nil, nil, fmt.Errorf("write id: %w", err)
		}
	}

	// For gzip, we need to close the writer before we can rewrite the header
	// So we'll write the header at the start with the correct count
	// Actually, since gzip is a stream format, we can't seek back
	// We need to write the header correctly from the start
	// Let's restructure to know the count first

	// Close writers and file
	if compress {
		// gzWriter was deferred
	}
	file.Close()

	// Since we collected all data in memory first, we can now write the file properly
	// Rewrite with correct header
	file2, err := os.Create(tmpPath)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("create file for rewrite: %w", err)
	}

	if compress {
		gzWriter := gzip.NewWriter(file2)
		writer = newBinaryWriter(gzWriter)
		if err := writer.writeHeader(uint32(count)); err != nil {
			gzWriter.Close()
			file2.Close()
			return 0, nil, nil, fmt.Errorf("write header: %w", err)
		}
		for _, pos := range positions {
			if err := writer.writePosition(pos); err != nil {
				gzWriter.Close()
				file2.Close()
				return 0, nil, nil, fmt.Errorf("write position: %w", err)
			}
		}
		for _, id := range ids {
			if err := writer.writeID(id); err != nil {
				gzWriter.Close()
				file2.Close()
				return 0, nil, nil, fmt.Errorf("write id: %w", err)
			}
		}
		gzWriter.Close()
	} else {
		writer = newBinaryWriter(file2)
		if err := writer.writeHeader(uint32(count)); err != nil {
			file2.Close()
			return 0, nil, nil, fmt.Errorf("write header: %w", err)
		}
		for _, pos := range positions {
			if err := writer.writePosition(pos); err != nil {
				file2.Close()
				return 0, nil, nil, fmt.Errorf("write position: %w", err)
			}
		}
		for _, id := range ids {
			if err := writer.writeID(id); err != nil {
				file2.Close()
				return 0, nil, nil, fmt.Errorf("write id: %w", err)
			}
		}
	}
	file2.Close()

	// Atomic rename
	if err := os.Rename(tmpPath, binPath); err != nil {
		return 0, nil, nil, fmt.Errorf("rename temp file: %w", err)
	}

	metadata.SystemCount = count
	return count, metadata, ids, nil
}

type positionData struct {
	X, Y, Z float32
}

type binaryWriter struct {
	w interface{ Write([]byte) (int, error) }
}

func newBinaryWriter(w interface{ Write([]byte) (int, error) }) *binaryWriter {
	return &binaryWriter{w: w}
}

func (b *binaryWriter) writeHeader(count uint32) error {
	header := make([]byte, HeaderSize)
	copy(header[0:4], Magic)
	binary.LittleEndian.PutUint32(header[4:8], Version)
	binary.LittleEndian.PutUint32(header[8:12], count)
	binary.LittleEndian.PutUint32(header[12:16], uint32(time.Now().Unix()))
	_, err := b.w.Write(header)
	return err
}

func (b *binaryWriter) writePosition(pos positionData) error {
	buf := make([]byte, PositionSize)
	binary.LittleEndian.PutUint32(buf[0:4], floatToUint32(pos.X))
	binary.LittleEndian.PutUint32(buf[4:8], floatToUint32(pos.Y))
	binary.LittleEndian.PutUint32(buf[8:12], floatToUint32(pos.Z))
	_, err := b.w.Write(buf)
	return err
}

func (b *binaryWriter) writeID(id uint64) error {
	buf := make([]byte, IDSize)
	binary.LittleEndian.PutUint64(buf, id)
	_, err := b.w.Write(buf)
	return err
}

func floatToUint32(f float32) uint32 {
	return math.Float32bits(f)
}

func writeMetadata(path string, metadata *Metadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// exportHistory queries cycle_snapshots and builds history.json.
// ids is the ordered slice of system id64 values from the binary export,
// used to build the id64→index reverse map.
func exportHistory(ctx context.Context, dsn string, outputPath string, ids []uint64, numCycles int, minCycle int) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to EDDN TSDB: %w", err)
	}
	defer pool.Close()

	// Build id64 → binary-file index map
	id64ToIndex := make(map[int64]int, len(ids))
	for i, id := range ids {
		id64ToIndex[int64(id)] = i
	}
	log.Printf("Built id64→index map: %d systems", len(id64ToIndex))

	// Query all cycle snapshots
	rows, err := pool.Query(ctx, `
		SELECT cycle_start, system_id64, controlling_power, powerplay_state
		FROM powerplay.cycle_snapshots
		WHERE cycle_start >= NOW() - make_interval(weeks => $1)
		ORDER BY cycle_start DESC, system_id64
	`, numCycles)
	if err != nil {
		return fmt.Errorf("query cycle_snapshots: %w", err)
	}
	defer rows.Close()

	// Group by cycle
	cycleData := make(map[time.Time]*HistoryCycle)
	var cycleOrder []time.Time
	unmapped := 0

	for rows.Next() {
		var cycleStart time.Time
		var systemID64 int64
		var power, state *string

		if err := rows.Scan(&cycleStart, &systemID64, &power, &state); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}

		idx, ok := id64ToIndex[systemID64]
		if !ok {
			unmapped++
			continue // System not in binary file (rare — only if Memgraph and TSDB are out of sync)
		}

		cycle, exists := cycleData[cycleStart]
		if !exists {
			cycle = &HistoryCycle{
				CycleStart: cycleStart,
				Powers:     make(map[string][]int),
				States:     make(map[string][]int),
			}
			cycleData[cycleStart] = cycle
			cycleOrder = append(cycleOrder, cycleStart)
		}

		if power != nil && *power != "" {
			cycle.Powers[*power] = append(cycle.Powers[*power], idx)
		}
		if state != nil && *state != "" {
			cycle.States[*state] = append(cycle.States[*state], idx)
		}
	}

	if unmapped > 0 {
		log.Printf("  %d snapshot rows had no matching system in binary export (skipped)", unmapped)
	}

	// Build ordered cycles with tick numbers
	history := History{
		GeneratedAt: time.Now().UTC(),
		Cycles:      make([]HistoryCycle, 0, len(cycleOrder)),
	}

	skipped := 0
	for _, cs := range cycleOrder {
		cycle := cycleData[cs]
		cycle.Tick = pp2CycleNumber(cs)

		// Filter out cycles before the minimum quality threshold
		if cycle.Tick < minCycle {
			skipped++
			continue
		}

		totalSystems := 0
		for _, indices := range cycle.Powers {
			totalSystems += len(indices)
		}
		log.Printf("  Cycle %d (%s): %d systems, %d powers, %d states",
			cycle.Tick, cs.Format("2006-01-02"), totalSystems,
			len(cycle.Powers), len(cycle.States))

		history.Cycles = append(history.Cycles, *cycle)
	}
	if skipped > 0 {
		log.Printf("  Skipped %d cycles before minimum cycle %d", skipped, minCycle)
	}

	// Write history.json
	data, err := json.Marshal(history)
	if err != nil {
		return fmt.Errorf("marshal history: %w", err)
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return fmt.Errorf("write history: %w", err)
	}

	log.Printf("  history.json: %d cycles, %.1f KB", len(history.Cycles), float64(len(data))/1024)
	return nil
}

func writeManifest(path string, count int, files []string) error {
	manifest := Manifest{
		Version:     Version,
		SystemCount: count,
		GeneratedAt: time.Now().UTC(),
		Files:       files,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}
