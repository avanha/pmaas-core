package configstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

type ConfigStore struct {
	dir string
}

func NewConfigStore() *ConfigStore {
	return &ConfigStore{
		dir: storeDir(),
	}
}

func (cs *ConfigStore) Save(pluginType reflect.Type, data any) error {
	// 1. Marshal the raw payload first to ensure it's valid JSON
	payloadBytes, err := json.Marshal(data)

	if err != nil {
		return fmt.Errorf("failed to marshal payload for %s: %w", pluginType, err)
	}

	dataType := reflect.TypeOf(data)

	envelope := Envelope{
		SaveTime:    time.Now().UTC(),
		PayloadType: dataType.PkgPath() + "/" + dataType.Name(),
		Payload:     json.RawMessage(payloadBytes),
	}

	// 3. Serialize the full envelope
	envelopeBytes, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}

	// 4. Ensure the target directory exists
	if err := os.MkdirAll(cs.dir, 0755); err != nil {
		return fmt.Errorf("failed to create store directory: %w", err)
	}

	sanitizedPluginName := sanitizePluginName(pluginType)

	// 5. Write to temp file
	tempFile, err := os.CreateTemp(cs.dir, fmt.Sprintf(".tmp-%s-*", sanitizedPluginName))

	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	tempFileName := tempFile.Name()

	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempFileName)
	}()

	if _, err := tempFile.Write(envelopeBytes); err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}

	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("failed to fsync temp file: %w", err)
	}

	// Close file explicitly before atomic swap (required for Windows support)
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	targetPath := filepath.Join(cs.dir, fmt.Sprintf("%s.json", sanitizedPluginName))

	// 5. ATOMIC SWAP: POSIX guarantees this atomic swap step
	if err := os.Rename(tempFileName, targetPath); err != nil {
		return fmt.Errorf("failed to perform atomic rename: %w", err)
	}

	fmt.Printf("Saved config to %s", targetPath)

	return nil
}

func (cs *ConfigStore) Load(pluginType reflect.Type, targetFactoryFunc func(string) any) (any, error) {
	// 1. Read the envelope file off disk
	filePath := filepath.Join(cs.dir, fmt.Sprintf("%s.json", sanitizePluginName(pluginType)))

	data, err := os.ReadFile(filePath)

	if err != nil {
		return nil, fmt.Errorf("failed to read config file for %s: %w", pluginType, err)
	}

	// 2. First pass: Decode the outer envelope
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("corrupted envelope structure in %s: %w", filePath, err)
	}

	target := targetFactoryFunc(envelope.PayloadType)

	if target == nil {
		return nil, fmt.Errorf("payload type %s not found in payload map", envelope.PayloadType)
	}

	if err := json.Unmarshal(envelope.Payload, target); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return target, nil
}

func sanitizePluginName(pluginType reflect.Type) string {
	return strings.ReplaceAll(strings.ReplaceAll(pluginType.String(), "-", "_"), ".", "_")
}

func storeDir() string {
	dir, err := os.UserConfigDir()

	if err != nil {
		panic(fmt.Errorf("Unable to obtain config dir: %w\n", err))
	}

	return filepath.Join(dir, "dir", "PMAAS")
}
