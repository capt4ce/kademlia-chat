package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"

	"github.com/libp2p/go-libp2p-core/host"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	peer "github.com/libp2p/go-libp2p-peer"
	peerstore "github.com/libp2p/go-libp2p-peerstore"

	ipfsaddr "github.com/ipfs/go-ipfs-addr"
	"github.com/libp2p/go-libp2p"
	net "github.com/libp2p/go-libp2p-net"
)

var (
	p2p       host.Host
	err       error
	lastError string
	gid       string

	writeToPeers  []peer.ID
	readFromPeers []peer.ID

	readWriters []*bufio.ReadWriter
)
var bootstrapPeers = []string{
	"/ip4/104.131.131.82/tcp/4001/ipfs/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
	"/ip4/104.236.179.241/tcp/4001/ipfs/QmSoLPppuBtQSGwKDZT2M73ULpjvfd3aZ6ha4oFGL1KrGM",
	"/ip4/104.236.76.40/tcp/4001/ipfs/QmSoLV4Bbm51jM9C4gDYZQ9Cy3U6aXMJDAbzgu2fzaDs64",
	"/ip4/128.199.219.111/tcp/4001/ipfs/QmSoLSafTMBsPKadTEgaXctDQVcqN88CNLHXMkTNwMKPnu",
	"/ip4/178.62.158.247/tcp/4001/ipfs/QmSoLer265NRgSp2LA3dPaeykiS1J6DifTC88f5uVQKNAd",
}

func main() {
	gidString := flag.String("r", gid, "Unique string to identify group of nodes. Share this with your friends to let them connect with you")
	flag.Parse()

	// Set flags for logging
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	ctx := context.Background()
	// create p2p instance
	p2p, err = libp2p.New(ctx, libp2p.DisableRelay())
	if err != nil {
		panic(fmt.Sprintf("failed to start p2p instance: %v", err))
	}
	log.Printf("Starting chat for peer %q ...\n", p2p.ID())

	// assign stream handler to the p2p instance
	p2p.SetStreamHandler("/chat/1.1.0", handleStream)

	// create DHT table
	kademliaDHT, err := dht.New(ctx, p2p)
	if err != nil {
		panic(fmt.Sprintf("failed create Kademlia DHT: %v", err))
	}

	// connecting to bootstrap nodes
	for _, peerAddr := range bootstrapPeers {
		pAddr, _ := ipfsaddr.ParseString(peerAddr)
		peerInfo, _ := peerstore.InfoFromP2pAddr(pAddr.Multiaddr())

		if err := p2p.Connect(ctx, *peerInfo); err != nil {
			log.Println("p2p connect error: ", err)
		}
	}

	// setting a groupd ID to be announced or joined
	cidBuilder := cid.V1Builder{Codec: cid.Raw, MhType: multihash.SHA2_256}
	groupPoint, _ := cidBuilder.Sum([]byte(*gidString))

	// setup p2p provider with the droupPoint
	tctx, cancel := context.WithTimeout(ctx, time.Second+10)
	defer cancel()
	if err := kademliaDHT.Provide(tctx, groupPoint, true); err != nil {
		panic(fmt.Sprintf("failed create Kademlia DHT: %v", err))
	}

	// code that acts like a client
	// start a go routine to detect peers that is belong to the same "announced string"
	go func() {
		// appending the peer to the peerToWrite list if the peer does not exist yet
		// creating a stream communication with the peer if the peer is not in our list yet
		for {
			tctx, cancel := context.WithTimeout(ctx, time.Second*10)
			peers, err := kademliaDHT.FindProviders(tctx, groupPoint)
			if err != nil {
				panic(fmt.Sprintf("failed to find providers: %v", err))
			}

			for _, peer := range peers {
				// ignore the current host and peer without address
				if peer.ID == p2p.ID() || len(peer.Addrs) == 0 {
					continue
				}

				isPeerAlreadyExist := false
				for _, writeConnection := range writeToPeers {
					if writeConnection.Pretty() == peer.ID.Pretty() {
						isPeerAlreadyExist = true
					}
				}

				// adding the peer to the peers to write, if it doesn't exists yet
				if !isPeerAlreadyExist {
					stream, err := p2p.NewStream(ctx, peer.ID, "/chat/1.1.0")
					if err != nil {
						log.Printf("ERROR creating new stream for (%s): %v\n", peer.ID, err)
					} else {
						writeToPeers = append(writeToPeers, stream.Conn().RemotePeer())
						// Create a buffer stream for non blocking read and write
						rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))
						// Add new buffer to write to
						readWriters = append(readWriters, rw)

						// Go routine to process stream lines
						go readData(rw)

						log.Printf("\r<main>Inbound Connections: %d Outbound Connections: %d ",
							len(readFromPeers), len(writeToPeers))
					}
				}
			}

			cancel()
		}
	}()

	select {}
}

// handleStream manages new incoming streams
func handleStream(stream net.Stream) {

	// Add new remote peer as peer to read from
	readFromPeers = append(readFromPeers, stream.Conn().RemotePeer())

	// Create a buffer stream for non blocking read and write
	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))

	// Go routine to process stream lines
	go readData(rw)

	// Go routine to write lines
	go writeData()

	// Shows the number of saved peers and connections, respectively
	fmt.Printf("\r<stream>Inbound Connections: %d Outbound Connections: %d ",
		len(readFromPeers), len(writeToPeers))

	// 'stream' will stay open until you close it (or the other side closes it).
}

func readData(rw *bufio.ReadWriter) {
	// Continuously waiting for incoming lines
	for {
		str, err := rw.ReadString('\n')
		if err != nil {
			if err.Error() != lastError {
				log.Println("ERROR: ", err)
				lastError = err.Error()
			}
			continue
		}

		if str == "" {
			continue
		}

		if str != "\n" {
			// Green console colour: 	\x1b[32m
			// Reset console colour: 	\x1b[0m

			// By default the sender's peer id was sent at the beginning of every line
			fmt.Printf("\n\x1b[32m%s\x1b[0m", str)
			fmt.Printf("%s ", p2p.ID())
		}
	}
}

func writeData() {

	// Buffer reading from chat
	stdReader := bufio.NewReader(os.Stdin)

	// Keep reading
	for {

		// Wait and read last line
		line, err := stdReader.ReadString('\n')
		if err != nil {
			panic(err)
		}

		// Set default prompt
		fmt.Print(p2p.ID(), " ")

		// An empty line writes a prompt locally but does not send anything
		if strings.Trim(line, "\n") == "" {
			continue
		}

		// Loop over all connected writers
		for _, rx := range readWriters {

			// Write sender's ID and the last line written
			rx.WriteString(fmt.Sprintf("%v %s", p2p.ID(), line))
			rx.Flush()
		}
	}
}
