package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func browserCommand(args []string) error {
	if len(args) == 0 || args[0] != "inspect" {
		return errors.New("usage: contextbridge browser inspect [--config path] [--tab ID]")
	}
	flags := flag.NewFlagSet("browser inspect", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "local config path")
	tabID := flags.Int("tab", 0, "selected browser tab ID; optional when only one tab is paired")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodGet, baseURL(cfg)+"/v1/status", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	response, err := (&http.Client{Timeout: 8 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("local bridge is not reachable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("local bridge returned %s", response.Status)
	}
	var status struct {
		Browser bridge.BrowserClientStatus `json:"browser"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 24<<20)).Decode(&status); err != nil {
		return err
	}
	if !status.Browser.Connected || len(status.Browser.Tabs) == 0 {
		return errors.New("no paired browser tab is connected")
	}
	if *tabID == 0 && len(status.Browser.Tabs) != 1 {
		ids := make([]string, 0, len(status.Browser.Tabs))
		for _, tab := range status.Browser.Tabs {
			ids = append(ids, strconv.Itoa(tab.ID))
		}
		return fmt.Errorf("choose a tab with --tab; paired IDs: %s", strings.Join(ids, ", "))
	}
	for _, tab := range status.Browser.Tabs {
		if *tabID != 0 && tab.ID != *tabID {
			continue
		}
		if tab.DOM == nil {
			return errors.New("the extension has not reported a DOM snapshot yet; retry after its next heartbeat")
		}
		result := struct {
			TabID   int                        `json:"tab_id"`
			Title   string                     `json:"title"`
			Profile string                     `json:"profile"`
			DOM     *bridge.BrowserDOMSnapshot `json:"dom"`
		}{TabID: tab.ID, Title: tab.Title, Profile: tab.Profile, DOM: tab.DOM}
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	return fmt.Errorf("paired browser tab %d was not found", *tabID)
}
