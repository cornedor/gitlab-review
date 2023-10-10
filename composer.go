package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"strconv"
)

func BackupVendor(workingDir string, tempDir string) bool {
	fmt.Println("Moving vendor to ", tempDir)
	err := os.Rename(path.Join(workingDir, "vendor"), path.Join(tempDir, "vendor"))

	if err != nil {
		return false
	}
	return true
}

func RestoreVendor(workingDir string, tempDir string) {
	vendorBackupDir := path.Join(tempDir, "vendor")
	if _, err := os.Stat(vendorBackupDir); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("node_modules backup not found, leaving current state intact.")
		} else {
			fmt.Println("Unkown error while restoring vendor, you might need to reinstall them.")
		}
	} else {
		fmt.Println("Restoring vendor from backup")
		os.RemoveAll(path.Join(workingDir, "vendor"))
		os.Rename(vendorBackupDir, path.Join(workingDir, "vendor"))
	}
}

func ComposerInstall(workingDir string, grCacheDir string, projectId int, isDdev bool) string {
	cacheDir := path.Join(grCacheDir, "composer", strconv.Itoa(projectId))
	os.MkdirAll(cacheDir, 0750)
	err := os.Rename(cacheDir, path.Join(workingDir, "vendor"))
	if err != nil {
		log.Println("Could not use cache %w", err)
	}

	cmd := exec.Command("composer", "install", "--prefer-offline")
	if isDdev {
		cmd = exec.Command("ddev", "composer", "install", "--prefer-offline")
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		log.Println("Could not install composer dependencies")
	}

	return cacheDir
}

func RestoreVendorCache(workingDir string, cacheDir string) {
	err := os.Rename(path.Join(workingDir, "vendor"), cacheDir)

	if err != nil {
		log.Println("Could not save vendor to cache %w", err)
	}
}
