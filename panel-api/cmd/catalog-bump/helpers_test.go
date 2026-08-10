package main

import "os"

func createApp(dir, appYaml string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(dir+"/app.yaml", []byte(appYaml), 0o644)
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
