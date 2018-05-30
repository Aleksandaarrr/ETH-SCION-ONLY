// Copyright 2015 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

// bootnode runs a bootstrap node for the Ethereum Discovery Protocol.
package main

import (
	"crypto/ecdsa"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/cmd/utils"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/discv5"
	"github.com/ethereum/go-ethereum/p2p/nat"
	"github.com/ethereum/go-ethereum/p2p/netutil"

	// SCION
	"github.com/scionproto/scion/go/lib/snet"
)

func main() {
	var (
		listenAddr   = flag.String("addr", ":30301", "listen address")
		genKey       = flag.String("genkey", "", "generate a node key")
		writeAddr    = flag.Bool("writeaddress", false, "write out the node's pubkey hash and quit")
		nodeKeyFile  = flag.String("nodekey", "", "private key filename")
		nodeKeyHex   = flag.String("nodekeyhex", "", "private key as hex (for testing)")
		natdesc      = flag.String("nat", "none", "port mapping mechanism (any|none|upnp|pmp|extip:<IP>)")
		netrestrict  = flag.String("netrestrict", "", "restrict network communication to the given IP networks (CIDR masks)")
		runv5        = flag.Bool("v5", false, "run a v5 topic discovery bootnode")
		verbosity    = flag.Int("verbosity", int(log.LvlInfo), "log verbosity (0-9)")
		vmodule      = flag.String("vmodule", "", "log verbosity pattern")
		scionAddr    *snet.Addr
		scionAddrStr string

		nodeKey *ecdsa.PrivateKey
		err     error
	)

	flag.StringVar(&scionAddrStr, "scion", "", "(Mandatory) address to listen on")
	flag.Parse()

	glogger := log.NewGlogHandler(log.StreamHandler(os.Stderr, log.TerminalFormat(false)))
	glogger.Verbosity(log.Lvl(*verbosity))
	glogger.Vmodule(*vmodule)
	log.Root().SetHandler(glogger)

	natm, err := nat.Parse(*natdesc)
	if err != nil {
		utils.Fatalf("-nat: %v", err)
	}
	switch {
	case *genKey != "":
		nodeKey, err = crypto.GenerateKey()
		if err != nil {
			utils.Fatalf("could not generate key: %v", err)
		}
		if err = crypto.SaveECDSA(*genKey, nodeKey); err != nil {
			utils.Fatalf("%v", err)
		}
		return
	case *nodeKeyFile == "" && *nodeKeyHex == "":
		utils.Fatalf("Use -nodekey or -nodekeyhex to specify a private key")
	case *nodeKeyFile != "" && *nodeKeyHex != "":
		utils.Fatalf("Options -nodekey and -nodekeyhex are mutually exclusive")
	case *nodeKeyFile != "":
		if nodeKey, err = crypto.LoadECDSA(*nodeKeyFile); err != nil {
			utils.Fatalf("-nodekey: %v", err)
		}
	case *nodeKeyHex != "":
		if nodeKey, err = crypto.HexToECDSA(*nodeKeyHex); err != nil {
			utils.Fatalf("-nodekeyhex: %v", err)
		}
	}

	if *writeAddr {
		fmt.Printf("%v\n", discover.PubkeyID(&nodeKey.PublicKey))
		os.Exit(0)
	}

	var restrictList *netutil.Netlist
	if *netrestrict != "" {
		restrictList, err = netutil.ParseNetlist(*netrestrict)
		if err != nil {
			utils.Fatalf("-netrestrict: %v", err)
		}
	}

	addr, err := net.ResolveUDPAddr("udp", *listenAddr)
	//fmt.Println(*listenAddr)
	//fmt.Println(addr)
	if err != nil {
		utils.Fatalf("-ResolveUDPAddr: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	//fmt.Println(conn)
	if err != nil {
		utils.Fatalf("-ListenUDP: %v", err)
	}

	// Setup SCION UDP socket
	var sconn *snet.Conn
	if scionAddrStr != "" {
		scionAddr, err = snet.AddrFromString(scionAddrStr)
		if err != nil {
			utils.Fatalf("Scion address needs to be specified with -scion: %v", err)
		} else {
			sciondAddr := "/run/shm/sciond/sd" + strconv.Itoa(int(scionAddr.IA.I)) + "-" + strconv.Itoa(int(scionAddr.IA.A)) + ".sock"
			dispatcherAddr := "/run/shm/dispatcher/default.sock"
			snet.Init(scionAddr.IA, sciondAddr, dispatcherAddr)

			// Listen on socket
			var err error
			sconn, err = snet.ListenSCION("udp4", scionAddr)
			if err != nil {
				utils.Fatalf("-SCION ListenUDP: %v", err)
			}
		}
	}

	realaddr := conn.LocalAddr().(*net.UDPAddr)
	if natm != nil {
		if !realaddr.IP.IsLoopback() {
			go nat.Map(natm, nil, "udp", realaddr.Port, realaddr.Port, "ethereum discovery")
		}
		// TODO: react to external IP changes over time.
		if ext, err := natm.ExternalIP(); err == nil {
			realaddr = &net.UDPAddr{IP: ext, Port: realaddr.Port}
		}
	}

	if *runv5 {
		fmt.Println("DISCV5 ListenUDP Bootnode main.go")
		if _, err := discv5.ListenUDP(nodeKey, conn, realaddr, "", restrictList); err != nil {
			utils.Fatalf("%v", err)
		}
	} else {
		cfg := discover.Config{
			PrivateKey:        nodeKey,
			AnnounceAddr:      realaddr,
			AnnounceAddrSCION: scionAddr.String(), // SCION
			NetRestrict:       restrictList,
		}
		fmt.Println("DISCOVER ListenUDP Bootnode main.go")
		if scionAddr != nil && sconn != nil {
			// ListenUDP SCION
			_, err := discover.ListenUDPSCION(conn, sconn, cfg)
			if err != nil {
				utils.Fatalf("%v", err)
			}
		} else {
			// ListenUDP
			if _, err := discover.ListenUDP(conn, cfg); err != nil {
				utils.Fatalf("%v", err)
			}
		}
	}

	select {}
}