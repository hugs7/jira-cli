package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hugs7/jira-cli/internal/api"
	"github.com/hugs7/jira-cli/internal/config"
)

func newAPICmd() *cobra.Command {
	var hostFlag string
	var method string
	var bodyFile string
	var rawBody string

	c := &cobra.Command{
		Use:   "api <endpoint>",
		Short: "Call any Jira REST endpoint and print the response",
		Long: "Pass a path relative to the configured API base (e.g. \"myself\"), " +
			"or a full URL. The response body is printed verbatim, " +
			"pretty-printed if it parses as JSON.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Get()
			host := hostFlag
			if host == "" {
				host = cfg.DefaultHost
			}
			if host == "" {
				return fmt.Errorf("no host configured — run `jr auth login`")
			}
			hcfg, ok := cfg.Hosts[host]
			if !ok {
				return fmt.Errorf("no auth for host %q", host)
			}
			client := api.New(host, hcfg)

			var body io.Reader
			switch {
			case bodyFile != "":
				f, err := os.Open(bodyFile)
				if err != nil {
					return err
				}
				defer f.Close()
				body = f
			case rawBody != "":
				body = strings.NewReader(rawBody)
			}

			req, err := client.NewRequest(strings.ToUpper(method), args[0], body)
			if err != nil {
				return err
			}
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			data, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			// Pretty-print JSON if possible.
			var pretty bytes.Buffer
			if json.Indent(&pretty, data, "", "  ") == nil {
				fmt.Println(pretty.String())
			} else {
				fmt.Println(string(data))
			}
			if resp.StatusCode >= 400 {
				return fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "Jira host to use")
	c.Flags().StringVarP(&method, "method", "X", "GET", "HTTP method")
	c.Flags().StringVar(&bodyFile, "body-file", "", "send the contents of this file as the request body")
	c.Flags().StringVar(&rawBody, "body", "", "send this string as the request body")
	return c
}
