package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"plandex/types"
	"strings"
	"sync"

	"github.com/plandex/plandex/shared"
	ignore "github.com/sabhiram/go-gitignore"
)

var Cwd string
var PlandexDir string
var ProjectRoot string
var HomePlandexDir string
var CacheDir string

var HomeDir string
var HomeAuthPath string
var HomeAccountsPath string

func init() {
	var err error
	Cwd, err = os.Getwd()
	if err != nil {
		panic(err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		panic("Couldn't find home dir:" + err.Error())
	}
	HomeDir = home

	if os.Getenv("PLANDEX_ENV") == "development" {
		HomePlandexDir = filepath.Join(home, ".plandex-home-dev")
	} else {
		HomePlandexDir = filepath.Join(home, ".plandex-home")
	}

	// Create the home plandex directory if it doesn't exist
	err = os.MkdirAll(HomePlandexDir, os.ModePerm)
	if err != nil {
		panic(err)
	}

	CacheDir = filepath.Join(HomePlandexDir, "cache")
	HomeAuthPath = filepath.Join(HomePlandexDir, "auth.json")
	HomeAccountsPath = filepath.Join(HomePlandexDir, "accounts.json")

	err = os.MkdirAll(filepath.Join(CacheDir, "tiktoken"), os.ModePerm)
	if err != nil {
		panic(err)
	}
	err = os.Setenv("TIKTOKEN_CACHE_DIR", CacheDir)
	if err != nil {
		panic(err)
	}

	PlandexDir = findPlandex(Cwd)
	if PlandexDir != "" {
		ProjectRoot = Cwd
	}
}

func FindOrCreatePlandex() (string, bool, error) {
	PlandexDir = findPlandex(Cwd)
	if PlandexDir != "" {
		ProjectRoot = Cwd
		return PlandexDir, false, nil
	}

	// Determine the directory path
	var dir string
	if os.Getenv("PLANDEX_ENV") == "development" {
		dir = filepath.Join(Cwd, ".plandex-dev")
	} else {
		dir = filepath.Join(Cwd, ".plandex")
	}

	err := os.Mkdir(dir, os.ModePerm)
	if err != nil {
		return "", false, err
	}
	PlandexDir = dir
	ProjectRoot = Cwd

	return dir, true, nil
}

func ProjectRootIsGitRepo() bool {
	if ProjectRoot == "" {
		return false
	}

	return IsGitRepo(ProjectRoot)
}

func IsGitRepo(dir string) bool {
	isGitRepo := false

	if isCommandAvailable("git") {
		// check whether we're in a git repo
		cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")

		cmd.Dir = dir

		err := cmd.Run()

		if err == nil {
			isGitRepo = true
		}
	}

	return isGitRepo
}

func GetProjectPaths(baseDir string) (map[string]bool, *ignore.GitIgnore, error) {
	if ProjectRoot == "" {
		return nil, nil, fmt.Errorf("no project root found")
	}

	return GetPaths(baseDir, ProjectRoot)
}

func GetPaths(baseDir, currentDir string) (map[string]bool, *ignore.GitIgnore, error) {
	ignored, err := GetPlandexIgnore(currentDir)

	if err != nil {
		return nil, nil, err
	}

	paths := map[string]bool{}
	ignoredPaths := map[string]bool{}

	dirs := map[string]bool{}

	isGitRepo := IsGitRepo(baseDir)

	errCh := make(chan error)
	var mu sync.Mutex
	numRoutines := 0

	if isGitRepo {
		// combine `git ls-files` and `git ls-files --others --exclude-standard`
		// to get all files in the repo

		numRoutines++
		go func() {
			// get all tracked files in the repo
			cmd := exec.Command("git", "ls-files")
			cmd.Dir = baseDir
			out, err := cmd.Output()

			if err != nil {
				errCh <- fmt.Errorf("error getting files in git repo: %s", err)
				return
			}

			files := strings.Split(string(out), "\n")

			mu.Lock()
			defer mu.Unlock()
			for _, file := range files {
				absFile := filepath.Join(baseDir, file)
				relFile, err := filepath.Rel(currentDir, absFile)

				if err != nil {
					errCh <- fmt.Errorf("error getting relative path: %s", err)
					return
				}

				if ignored != nil && ignored.MatchesPath(relFile) {
					ignoredPaths[relFile] = true
					continue
				}

				paths[relFile] = true
			}

			errCh <- nil
		}()

		// get all untracked non-ignored files in the repo
		numRoutines++
		go func() {
			cmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
			cmd.Dir = baseDir
			out, err := cmd.Output()

			if err != nil {
				errCh <- fmt.Errorf("error getting untracked files in git repo: %s", err)
				return
			}

			files := strings.Split(string(out), "\n")

			mu.Lock()
			defer mu.Unlock()
			for _, file := range files {
				absFile := filepath.Join(baseDir, file)
				relFile, err := filepath.Rel(currentDir, absFile)

				if err != nil {
					errCh <- fmt.Errorf("error getting relative path: %s", err)
					return
				}

				if ignored != nil && ignored.MatchesPath(relFile) {
					ignoredPaths[relFile] = true
					continue
				}

				paths[relFile] = true
			}

			errCh <- nil
		}()
	}

	// get all paths in the directory
	numRoutines++
	go func() {
		err = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() {
				relPath, err := filepath.Rel(currentDir, path)
				if err != nil {
					return err
				}

				if ignored != nil && ignored.MatchesPath(relPath) {
					ignoredPaths[relPath] = true
					return filepath.SkipDir
				}

				dirs[relPath] = true
			} else if !isGitRepo {
				relPath, err := filepath.Rel(currentDir, path)
				if err != nil {
					return err
				}

				if ignored != nil && ignored.MatchesPath(relPath) {
					ignoredPaths[relPath] = true
					return nil
				}

				// lock isn't need here because isGitRepo is false, which makes this the only routine
				paths[relPath] = true
			}

			return nil
		})

		if err != nil {
			errCh <- fmt.Errorf("error walking directory: %s", err)
			return
		}

		errCh <- nil
	}()

	for i := 0; i < numRoutines; i++ {
		err := <-errCh
		if err != nil {
			return nil, nil, err
		}
	}

	for dir := range dirs {
		paths[dir] = true
	}

	return paths, ignored, nil

}

func GetPlandexIgnore(dir string) (*ignore.GitIgnore, error) {
	ignorePath := filepath.Join(dir, ".plandexignore")

	if _, err := os.Stat(ignorePath); err == nil {
		ignored, err := ignore.CompileIgnoreFile(ignorePath)

		if err != nil {
			return nil, fmt.Errorf("error reading .plandexignore file: %s", err)
		}

		return ignored, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("error checking for .plandexignore file: %s", err)
	}

	return nil, nil
}

func GetParentProjectIdsWithPaths() ([][2]string, error) {
	var parentProjectIds [][2]string
	currentDir := filepath.Dir(Cwd)

	for currentDir != "/" {
		plandexDir := findPlandex(currentDir)
		projectSettingsPath := filepath.Join(plandexDir, "project.json")
		if _, err := os.Stat(projectSettingsPath); err == nil {
			bytes, err := os.ReadFile(projectSettingsPath)
			if err != nil {
				return nil, fmt.Errorf("error reading projectId file: %s", err)
			}

			var settings types.CurrentProjectSettings
			err = json.Unmarshal(bytes, &settings)

			if err != nil {
				panic(fmt.Errorf("error unmarshalling project.json: %v", err))
			}

			projectId := string(settings.Id)
			parentProjectIds = append(parentProjectIds, [2]string{currentDir, projectId})
		}
		currentDir = filepath.Dir(currentDir)
	}

	return parentProjectIds, nil
}

func GetChildProjectIdsWithPaths(ctx context.Context) ([][2]string, error) {
	var childProjectIds [][2]string

	err := filepath.Walk(Cwd, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// if permission denied, skip the path
			if os.IsPermission(err) {
				if info.IsDir() {
					return filepath.SkipDir
				} else {
					return nil
				}
			}

			return err
		}

		if strings.HasPrefix(info.Name(), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context timeout")
		default:
		}

		if info.IsDir() && path != Cwd {
			plandexDir := findPlandex(path)
			projectSettingsPath := filepath.Join(plandexDir, "project.json")
			if _, err := os.Stat(projectSettingsPath); err == nil {
				bytes, err := os.ReadFile(projectSettingsPath)
				if err != nil {
					return fmt.Errorf("error reading projectId file: %s", err)
				}
				var settings types.CurrentProjectSettings
				err = json.Unmarshal(bytes, &settings)

				if err != nil {
					panic(fmt.Errorf("error unmarshalling project.json: %v", err))
				}

				projectId := string(settings.Id)
				childProjectIds = append(childProjectIds, [2]string{path, projectId})
			}
		}
		return nil
	})

	if err != nil {
		if err.Error() == "context timeout" {
			return childProjectIds, nil
		}

		return nil, fmt.Errorf("error walking the path %s: %s", Cwd, err)
	}

	return childProjectIds, nil
}

func GetBaseDirForContexts(contexts []*shared.Context) string {
	var paths []string

	for _, context := range contexts {
		if context.FilePath != "" {
			paths = append(paths, context.FilePath)
		}
	}

	return GetBaseDirForFilePaths(paths)
}

func GetBaseDirForFilePaths(paths []string) string {
	baseDir := ProjectRoot
	dirsUp := 0

	for _, path := range paths {
		currentDir := ProjectRoot

		pathSplit := strings.Split(path, string(os.PathSeparator))

		n := 0
		for _, p := range pathSplit {
			if p == ".." {
				n++
				currentDir = filepath.Dir(currentDir)
			} else {
				break
			}
		}

		if n > dirsUp {
			dirsUp = n
			baseDir = currentDir
		}
	}

	return baseDir
}

func findPlandex(baseDir string) string {
	var dir string
	if os.Getenv("PLANDEX_ENV") == "development" {
		dir = filepath.Join(baseDir, ".plandex-dev")
	} else {
		dir = filepath.Join(baseDir, ".plandex")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return dir
	}

	return ""
}

func isCommandAvailable(name string) bool {
	cmd := exec.Command(name, "--version")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}
