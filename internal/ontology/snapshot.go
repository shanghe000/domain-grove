package ontology

import (
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

func WriteSnapshot(path string, graph *Graph) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".ontology-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()

	encoder := yaml.NewEncoder(file)
	encoder.SetIndent(2)
	if err := encoder.Encode(graph.Data()); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if err := file.Chmod(0o444); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
