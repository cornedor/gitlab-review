package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"strconv"
)

func BackupNodeModules(workingDir string, tempDir string) {
	fmt.Println("Moving node_modules to", tempDir)
	os.Rename(path.Join(workingDir, "node_modules"), path.Join(tempDir, "node_modules"))
}

func RestoreNodeModules(workingDir string, tempDir string) {
	modulesBackupDir := path.Join(tempDir, "node_modules")
	if _, err := os.Stat(modulesBackupDir); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("node_modules backup not found, leaving current state intact.")
		} else {
			fmt.Println("Unkown error while restoring node_modules, you might need to reinstall them.")
		}
	} else {
		fmt.Println("Restoring node_modules from backup")
		os.RemoveAll(path.Join(workingDir, "node_modules"))
		os.Rename(modulesBackupDir, path.Join(workingDir, "node_modules"))
	}
}

func YarnInstall(workingDir string, grCacheDir string, projectId int) string {
	cacheDir := path.Join(grCacheDir, "yarn", strconv.Itoa(projectId))
	os.MkdirAll(cacheDir, 0750)
	// Symlink has issues in some projects
	// err := os.Symlink(cacheDir, path.Join(getWorkingDir(), "node_modules"))
	err := os.Rename(cacheDir, path.Join(workingDir, "node_modules"))
	if err != nil {
		log.Println("Could not use cache %w", err)
	}

	cmd := exec.Command("yarn", "install", "--prefer-offline")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		log.Println("Could not install yarn dependencies")
	}

	return cacheDir
}

func RestoreYarnCache(workingDir string, cacheDir string) {
	err := os.Rename(path.Join(workingDir, "node_modules"), cacheDir)

	if err != nil {
		log.Println("Could not save to cache %w", err)
	}
}
