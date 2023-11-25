package lib

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"plandex/format"
	"plandex/term"
	"plandex/types"
	"plandex/url"
	"strings"
	"sync"
	"time"

	"strconv"

	"github.com/briandowns/spinner"
	"github.com/olekukonko/tablewriter"
	"github.com/plandex/plandex/shared"
)

func MustLoadContext(resources []string, params *types.LoadContextParams) (int, int) {
	timeStart := time.Now()

	s := spinner.New(spinner.CharSets[33], 100*time.Millisecond)
	s.Prefix = "📥 Loading context... "
	s.Start()

	maxTokens := shared.MaxContextTokens

	planState, err := GetPlanState()
	if err != nil {
		s.Stop()
		term.ClearCurrentLine()
		fmt.Fprintf(os.Stderr, "Failed to get plan state: %v\n", err)
		os.Exit(1)
	}

	tokensAdded := 0
	totalTokens := planState.ContextTokens
	totalUpdatableTokens := planState.ContextUpdatableTokens
	var totalTokensMutex sync.Mutex

	var contextParts []*shared.ModelContextPart
	var contextPartsMutex sync.Mutex

	wg := sync.WaitGroup{}

	if params.Note != "" {
		wg.Add(1)

		go func() {
			defer wg.Done()

			body := params.Note
			numTokens, err := shared.GetNumTokens(body)
			if err != nil {
				s.Stop()
				term.ClearCurrentLine()
				fmt.Fprintf(os.Stderr, "Failed to get number of tokens for the note: %v\n", err)
				os.Exit(1)
			}

			totalTokensMutex.Lock()
			func() {
				defer totalTokensMutex.Unlock()

				totalTokens += numTokens
				tokensAdded += numTokens

				if totalTokens > maxTokens {
					s.Stop()
					term.ClearCurrentLine()
					fmt.Fprintf(os.Stderr, "🚨 The total number of tokens (%d) exceeds the maximum allowed (%d)\n", totalTokens, maxTokens)
					os.Exit(1)
				}
			}()

			hash := sha256.Sum256([]byte(body))
			sha := hex.EncodeToString(hash[:])

			fileNameResp, err := Api.FileName(body)
			if err != nil {
				s.Stop()
				term.ClearCurrentLine()
				fmt.Fprintf(os.Stderr, "Failed to get a file name for the text: %v\n", err)
				os.Exit(1)
			}

			fileName := format.GetFileNameWithoutExt(fileNameResp.FileName)

			ts := shared.StringTs()
			contextPart := &shared.ModelContextPart{
				Type:      shared.ContextNoteType,
				Name:      fileName,
				Body:      body,
				Sha:       sha,
				NumTokens: numTokens,
				AddedAt:   ts,
				UpdatedAt: ts,
			}

			contextPartsMutex.Lock()
			contextParts = append(contextParts, contextPart)
			contextPartsMutex.Unlock()

		}()

	}

	hasPipeData := false
	fileInfo, err := os.Stdin.Stat()
	if err != nil {
		s.Stop()
		term.ClearCurrentLine()
		fmt.Fprintf(os.Stderr, "Failed to stat stdin: %v\n", err)
		os.Exit(1)
	}
	if fileInfo.Mode()&os.ModeNamedPipe != 0 {
		reader := bufio.NewReader(os.Stdin)
		pipedData, err := io.ReadAll(reader)
		if err != nil {
			s.Stop()
			term.ClearCurrentLine()
			fmt.Fprintf(os.Stderr, "Failed to read piped data: %v\n", err)
			os.Exit(1)
		}

		if len(pipedData) > 0 {
			wg.Add(1)

			hasPipeData = true

			go func() {
				defer wg.Done()

				body := string(pipedData)
				numTokens, err := shared.GetNumTokens(body)
				if err != nil {
					s.Stop()
					term.ClearCurrentLine()
					fmt.Fprintf(os.Stderr, "Failed to get number of tokens for the note: %v\n", err)
					os.Exit(1)
				}

				totalTokensMutex.Lock()
				func() {
					defer totalTokensMutex.Unlock()

					totalTokens += numTokens
					tokensAdded += numTokens
					if totalTokens > maxTokens {
						s.Stop()
						term.ClearCurrentLine()
						fmt.Fprintf(os.Stderr, "🚨 The total number of tokens (%d) exceeds the maximum allowed (%d)\n", totalTokens, maxTokens)
						os.Exit(1)
					}
				}()

				hash := sha256.Sum256([]byte(body))
				sha := hex.EncodeToString(hash[:])

				fileNameResp, err := Api.FileName(body)
				if err != nil {
					s.Stop()
					term.ClearCurrentLine()
					fmt.Fprintf(os.Stderr, "Failed to get a file name for piped data: %v\n", err)
					os.Exit(1)
				}

				fileName := format.GetFileNameWithoutExt(fileNameResp.FileName)

				ts := shared.StringTs()
				contextPart := &shared.ModelContextPart{
					Type:      shared.ContextPipedDataType,
					Name:      fileName,
					Body:      body,
					Sha:       sha,
					NumTokens: numTokens,
					AddedAt:   ts,
					UpdatedAt: ts,
				}

				contextPartsMutex.Lock()
				contextParts = append(contextParts, contextPart)
				contextPartsMutex.Unlock()

			}()
		}
	}

	var inputUrls []string
	var inputFilePaths []string

	if len(resources) > 0 {
		for _, resource := range resources {
			// so far resources are either files or urls
			if url.IsValidURL(resource) {
				inputUrls = append(inputUrls, resource)
			} else {
				inputFilePaths = append(inputFilePaths, resource)
			}
		}
	}

	if len(inputFilePaths) > 0 {
		if params.NamesOnly {
			for _, inputFilePath := range inputFilePaths {
				wg.Add(1)

				go func(inputFilePath string) {
					defer wg.Done()

					flattenedPaths, err := ParseInputPaths([]string{inputFilePath}, params)
					if err != nil {
						s.Stop()
						term.ClearCurrentLine()
						fmt.Fprintf(os.Stderr, "Failed to parse input paths: %v\n", err)
						os.Exit(1)
					}

					body := strings.Join(flattenedPaths, "\n")
					bytes := []byte(body)

					hash := sha256.Sum256(bytes)
					sha := hex.EncodeToString(hash[:])
					numTokens, err := shared.GetNumTokens(body)
					if err != nil {
						s.Stop()
						term.ClearCurrentLine()
						fmt.Fprintf(os.Stderr, "Failed to get number of tokens for the note: %v\n", err)
						os.Exit(1)
					}

					totalTokensMutex.Lock()
					func() {
						defer totalTokensMutex.Unlock()
						totalTokens += numTokens
						totalUpdatableTokens += numTokens
						tokensAdded += numTokens
						if totalTokens > maxTokens {
							s.Stop()
							term.ClearCurrentLine()
							fmt.Fprintf(os.Stderr, "🚨 The total number of tokens (%d) exceeds the maximum allowed (%d)\n", totalTokens, maxTokens)
							os.Exit(1)
						}

					}()

					ts := shared.StringTs()

					// get last portion of directory path
					name := filepath.Base(inputFilePath)
					if name == "." {
						name = "cwd"
					}
					if name == ".." {
						name = "parent"
					}

					contextPart := &shared.ModelContextPart{
						Type:      shared.ContextDirectoryTreeType,
						Name:      inputFilePath,
						FilePath:  inputFilePath,
						Body:      body,
						Sha:       sha,
						NumTokens: numTokens,
						AddedAt:   ts,
						UpdatedAt: ts,
					}

					contextPartsMutex.Lock()
					contextParts = append(contextParts, contextPart)
					contextPartsMutex.Unlock()

				}(inputFilePath)
			}

		} else {
			flattenedPaths, err := ParseInputPaths(inputFilePaths, params)
			if err != nil {
				s.Stop()
				term.ClearCurrentLine()
				fmt.Fprintf(os.Stderr, "Failed to parse input paths: %v\n", err)
				os.Exit(1)
			}

			for _, path := range flattenedPaths {
				wg.Add(1)

				go func(path string) {
					defer wg.Done()

					fileContent, err := os.ReadFile(path)
					if err != nil {
						s.Stop()
						term.ClearCurrentLine()
						fmt.Fprintf(os.Stderr, "Failed to read the file %s: %v", path, err)
						os.Exit(1)
					}

					body := string(fileContent)
					hash := sha256.Sum256(fileContent)
					sha := hex.EncodeToString(hash[:])
					numTokens, err := shared.GetNumTokens(body)
					if err != nil {
						s.Stop()
						term.ClearCurrentLine()
						fmt.Fprintf(os.Stderr, "Failed to get number of tokens for the note: %v\n", err)
						os.Exit(1)
					}

					totalTokensMutex.Lock()
					func() {
						defer totalTokensMutex.Unlock()
						totalTokens += numTokens
						tokensAdded += numTokens
						totalUpdatableTokens += numTokens
						if totalTokens > maxTokens {
							s.Stop()
							term.ClearCurrentLine()
							fmt.Fprintf(os.Stderr, "🚨 The total number of tokens (%d) exceeds the maximum allowed (%d)\n", totalTokens, maxTokens)
							os.Exit(1)
						}

					}()

					ts := shared.StringTs()

					contextPart := &shared.ModelContextPart{
						Type:      shared.ContextFileType,
						Name:      path,
						Body:      body,
						FilePath:  path,
						Sha:       sha,
						NumTokens: numTokens,
						AddedAt:   ts,
						UpdatedAt: ts,
					}

					contextPartsMutex.Lock()
					contextParts = append(contextParts, contextPart)
					contextPartsMutex.Unlock()

				}(path)

			}
		}

	}

	if len(inputUrls) > 0 {
		for _, u := range inputUrls {
			wg.Add(1)

			go func(u string) {
				defer wg.Done()

				body, err := url.FetchURLContent(u)
				if err != nil {
					s.Stop()
					term.ClearCurrentLine()
					fmt.Fprintf(os.Stderr, "Failed to fetch content from URL %s: %v", u, err)
					os.Exit(1)
				}

				numTokens, err := shared.GetNumTokens(body)
				if err != nil {
					s.Stop()
					term.ClearCurrentLine()
					fmt.Fprintf(os.Stderr, "Failed to get number of tokens for the note: %v\n", err)
					os.Exit(1)
				}

				totalTokensMutex.Lock()
				func() {
					defer totalTokensMutex.Unlock()
					totalTokens += numTokens
					tokensAdded += numTokens
					totalUpdatableTokens += numTokens
					if totalTokens > maxTokens {
						s.Stop()
						term.ClearCurrentLine()
						fmt.Fprintf(os.Stderr, "🚨 The total number of tokens (%d) exceeds the maximum allowed (%d)\n", totalTokens, maxTokens)
						os.Exit(1)
					}
				}()

				hash := sha256.Sum256([]byte(body))
				sha := hex.EncodeToString(hash[:])

				ts := shared.StringTs()

				name := url.SanitizeURL(u)
				// show the first 20 characters, then ellipsis then the last 20 characters of 'name'
				if len(name) > 40 {
					name = name[:20] + "⋯" + name[len(name)-20:]
				}

				contextPart := &shared.ModelContextPart{
					Type:      shared.ContextURLType,
					Name:      name,
					Url:       u,
					Body:      body,
					Sha:       sha,
					NumTokens: numTokens,
					AddedAt:   ts,
					UpdatedAt: ts,
				}

				contextPartsMutex.Lock()
				contextParts = append(contextParts, contextPart)
				contextPartsMutex.Unlock()
			}(u)
		}
	}

	wg.Wait()

	TableForLoadContext := func(contextParts []*shared.ModelContextPart) string {
		tableString := &strings.Builder{}
		table := tablewriter.NewWriter(tableString)
		table.SetHeader([]string{"Name", "Type", "🪙"})
		table.SetAutoWrapText(false)

		for _, part := range contextParts {
			t, icon := GetContextTypeAndIcon(part)
			row := []string{
				" " + icon + " " + part.Name,
				t,
				"+" + strconv.Itoa(part.NumTokens),
			}

			table.Rich(row, []tablewriter.Colors{
				{tablewriter.FgHiGreenColor, tablewriter.Bold},
				{tablewriter.FgHiGreenColor},
				{tablewriter.FgHiGreenColor},
			})
		}

		table.Render()

		return tableString.String()
	}

	if len(contextParts) == 0 {
		fmt.Println("🤷‍♂️ No context loaded")
		os.Exit(1)
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- writeContextParts(contextParts)
	}()

	go func() {
		planState.ContextTokens = totalTokens
		planState.ContextUpdatableTokens = totalUpdatableTokens
		errCh <- SetPlanState(planState, shared.StringTs())
	}()

	for i := 0; i < 2; i++ {
		err := <-errCh
		if err != nil {
			fmt.Printf("Failed to write context: %v\n", err)
			os.Exit(1)
		}
	}

	var added []string
	if params.Note != "" {
		added = append(added, "a note")
	}
	if hasPipeData {
		added = append(added, "piped data")
	}
	if len(inputFilePaths) > 0 {
		var label string
		if params.NamesOnly {
			label = "directory tree"
			if len(inputFilePaths) > 1 {
				label = "directory trees"
			}
		} else {
			label = "file"
			if len(inputFilePaths) > 1 {
				label = "files"
			}
		}

		added = append(added, fmt.Sprintf("%d %s", len(inputFilePaths), label))
	}
	if len(inputUrls) > 0 {
		label := "url"
		if len(inputUrls) > 1 {
			label = "urls"
		}
		added = append(added, fmt.Sprintf("%d %s", len(inputUrls), label))
	}

	msg := "Loaded "
	if len(added) <= 2 {
		msg += strings.Join(added, " and ")
	} else {
		for i, add := range added {
			if i == len(added)-1 {
				msg += ", and " + add
			} else {
				msg += ", " + add
			}
		}
	}
	msg += fmt.Sprintf(" into context | added → %d 🪙 |  total → %d 🪙", tokensAdded, totalTokens)

	if err != nil {
		s.Stop()
		term.ClearCurrentLine()
		fmt.Fprintf(os.Stderr, "Failed to get total tokens: %v\n", err)
		os.Exit(1)
	}

	if err != nil {
		s.Stop()
		term.ClearCurrentLine()
		fmt.Fprintf(os.Stderr, "Failed to commit context update to git: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(timeStart)
	if elapsed < 700*time.Millisecond {
		time.Sleep(700*time.Millisecond - elapsed)
	}

	s.Stop()
	term.ClearCurrentLine()
	fmt.Println("✅ " + msg)

	if len(contextParts) > 0 {
		tableString := TableForLoadContext(contextParts)

		err = GitCommitContextUpdate(msg + "\n\n" + tableString)
		if err != nil {
			s.Stop()
			term.ClearCurrentLine()
			fmt.Fprintf(os.Stderr, "Failed to commit context update to git: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(tableString)
	}

	return tokensAdded, totalTokens
}
