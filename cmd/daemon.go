/*
Copyright © 2023 David Araújo <davidaraujo98@github.io>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package cmd

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"sensormesh/cmd/shared"
	"strings"
	"time"

	orbitdb "berty.tech/go-orbit-db"
	"berty.tech/go-orbit-db/accesscontroller"
	"berty.tech/go-orbit-db/iface"
	client "github.com/ipfs/go-ipfs-http-client"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

var (
	ctx          context.Context
	cancel       context.CancelFunc
	storeAddress string
	logbuf       bytes.Buffer
	logger       zerolog.Logger
	logStore     iface.EventLogStore
)

func publish() {
	defer cancel()
	lastTriggerTime := time.Now().Unix()
	currentTime := lastTriggerTime
	for {
		currentTime = time.Now().Unix()

		// whisper every 10 seconds
		if currentTime-lastTriggerTime >= 30 {
			lastTriggerTime = currentTime

			// TODO - Get cam from Vanetza, use .RawJSON
			logger.Info().
				Str("type", "whisper").
				Str("name", shared.ViperConfs.GetString("name")).
				Send()

			// Posting new value to the log store
			_, err := logStore.Add(ctx, logbuf.Bytes())
			if err != nil {
				panic(fmt.Errorf("failed to put in log store: %s", err))
			}
		}

		// Reset reading buffer
		logbuf.Reset()
	}
}

func subscribe() {
	defer cancel()
	var lastValue []byte
	file, err := os.OpenFile(shared.ViperConfs.GetString("logfile"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(fmt.Errorf("failed to open log file "+shared.ViperConfs.GetString("logfile")+": %s", err))
	}
	fileWriter := log.New(file, "", 0)
	defer file.Close()
	fmt.Println("[+] Writing log to " + shared.ViperConfs.GetString("logfile"))
	for {
		//Reading the last value inserted in the log store
		op, err := logStore.List(ctx, &iface.StreamOptions{})
		if err != nil {
			panic(fmt.Errorf("failed to get list from log store: %s", err))
		}

		if len(op) > 0 && !reflect.DeepEqual(op[0].GetValue(), lastValue) {
			//Write to log file if the new value is different from the last.
			//Since we are using timestamps, all correct messages will be
			//different, so this method becomes reliable in avoiding
			//incorrect or duplicate messages
			lastValue = op[0].GetValue()
			fileWriter.Println(strings.TrimRight(string(op[0].GetValue()), "\n"))
		}
	}
}

// daemonCmd represents the daemon command
var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run a OrbitDB sensor logger",
	Long: `'sensormesh daemon' runs a persistent sensormesh daemon that can
query specified sensor and log their responses to a OrbitDB
log file, that will be shared between nodes in a same IPFS
private network.

The daemon will start by first configuring the current
machine as a node in a private IPFS network, and then
initialize IPFS's daemon`,
	PreRun: func(cmd *cobra.Command, args []string) {
		// Check existence of config file
		_, err := shared.Exists(shared.ConfigFilePath)
		if err != nil {
			panic(fmt.Errorf("configuration file not set. Try running 'sensormesh init' first: %s", err))
		}

		// Load configurations from the configurations file, if non-existing, a new one will be created
		shared.LoadConfigurationFromFile()

		// Connecting to local IPFS node API
		shell, err := client.NewURLApiWithClient(shared.LocalIPFSApiAddress(), &http.Client{})
		if err != nil {
			panic(fmt.Errorf("failed to connect to local IPFS API. IPFS daemon must be running with '--enable-pubsub-experiment': %s", err))
		}
		fmt.Println("[+] Connecting to " + shared.ViperConfs.GetString("name") + "'s local IPFS API at " + shared.LocalIPFSApiAddress())

		ctx, cancel = context.WithCancel(context.Background())

		// Initiating a new OrbitDB instance
		db, err := orbitdb.NewOrbitDB(ctx, shell, &orbitdb.NewOrbitDBOptions{})
		if err != nil {
			panic(fmt.Errorf("failed to create new orbitdb. IPFS daemon must be running with '--enable-pubsub-experiment': %s", err))
		}

		// Give read and write permissions to all
		ac := &accesscontroller.CreateAccessControllerOptions{
			Access: map[string][]string{
				"write": {
					"*",
				},
				"read": {
					"*",
				},
			},
		}

		// Tries connecting to the given address (If name was give, error will trigger creation on new database)
		logStore, err = db.Log(ctx, storeAddress, &orbitdb.CreateDBOptions{
			AccessController: ac,
		})
		if err != nil {
			panic(fmt.Errorf("failed to get log store: %s", err))
		}
		fmt.Printf("[🗸] Connected to %s store with address: %s\n", logStore.DBName(), logStore.Address().String())
		shared.ViperConfs.Set("orbitdb.storeaddress", logStore.Address().String())

		// Initialize zerolog logger
		logger = zerolog.New(&logbuf).With().Timestamp().Logger()

		err = shared.ViperConfs.WriteConfig()
		if err != nil {
			panic(fmt.Errorf("error updating config file: %v", err))
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		// Initiate reading and writing to the database as a multi-threaded processes
		go publish()
		go subscribe()

		fmt.Println("[+] Press Ctrl+c to stop daemon")

		// Capture SIGINT
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt)

		// Wait for either WaitGroup or interrupt signal
		<-sigint

		fmt.Println("\n[!] Interrupt signal received, terminating...")
	},
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.Flags().StringVar(&storeAddress, "storeaddress", "event", "Address of the log store. Defaults to create a new log store with name 'event'")
	_ = daemonCmd.MarkFlagRequired("name")
}
