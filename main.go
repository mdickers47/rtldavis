/*
rtldavis, an rtl-sdr receiver for Davis Instruments weather stations.
Copyright (C) 2015  Douglas Hall

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.

	Modified by Luc Heijst
	- March 2019
	Added: Multi-channel hopping
	Added: options  -ex, -u, -fc, -ppm, -gain, -tf, -tr, -maxmissed,
	                -startfreq, -endfreq, -stepfreq
	Removed: options -id, -v
	- May 20120
	v0.14:
	Added:   handling of NZ frequencies
	Changed: freqErrors are now stored for the right frequencies and transmitters
	v0.15:
	Added:   options -d, -v, -noafc; thanks to Steve Wormley for the code
	Changed: detection of freqError; thanks to Steve Wormley for the code
	Changed: hopping timing (changed receiveWindow from 10 to 300 ms)
*/
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/jpoirier/gortlsdr"
	"github.com/lheijst/rtldavis/protocol"
)

const maxTr = 8

var (
	// program settings
	ex              int     // -ex = extra loopTime in msex
	fc              int     // -fc = frequency correction for all channels
	ppm             int     // -ppm = frequency correction of rtl dongle in ppm
	gain            int     // -gain = tuner gain in tenths of a Db
	maxmissed       int     // -maxmisssed = max missed-packets-in-a-row before new init
	transmitterFreq *string // -tf = transmitter frequencies, EU, US or NZ.
	undefined       *bool   // -u = log undefined signals
	verbose         *bool   // -v = emit verbose debug messages
	disableAfc      *bool   // -noafc = disable any automatic corrections
	deviceString    *string // -d = device serial number or device index
	graphiteSrv     *string // -gs = decode the packets and send to graphite
	graphitePrefix  *string // -gp = prefix for graphite metrics
	// general
	actChan [maxTr]int // list with actual channels (0-7);
	//   next values are zero (non-meaning)
	msgIdToChan []int // msgIdToChan[id] is pointer to channel in actChan;
	//   non-defined id's have ptr value 9
	expectedChanPtr int   // pointer to actChan of next expected msg.Data
	curTime         int64 // current UTC-nanoseconds
	maxFreq         int   // number of frequencies (EU=5, US=51)
	maxChan         int   // number of defined (=actual) channels
	receiveWindow   int   // timespan in ms for receiving a message

	// per channel (index is actChan[ch])
	chLastVisits  [maxTr]int64   // last visit times in UTC-nanoseconds
	chNextVisits  [maxTr]int64   // next visit times (future) in UTC-nanoseconds
	chTotMsgs     [maxTr]int     // total received messages since startup
	chAlarmCnts   [maxTr]int     // numbers of missed-counts-in-a-row
	chLastHops    [maxTr]int     // last hop channel-ids (sequential order)
	chNextHops    [maxTr]int     // next hop channel-ids (sequential order)
	chMissPerFreq [maxTr][51]int // transmitter missed per frequency channel

	// per id (index is msg.ID)
	idLoopPeriods [maxTr]time.Duration // durations of one loop (higher IDs: longer durations)
	idUndefs      [maxTr]int           // number of received messages of undefined id's since startup

	// totals
	totInit int // total of init procedures since startup (first not counted)

	// hop and channel-frequency
	loopTimer      time.Time     // time when next hop sequence will time-out
	loopPeriod     time.Duration // period since now when next hop sequence will time-out
	actHopChanIdx  int           // channel-id of actual hop sequence (EU: 0-4, US and NZ: 0-50)
	nextHopChan    int           // channel-id of next hop
	nextHopTran    int           // transmitter-id of next hop
	channelFreq    int           // frequency of the channel to transmit
	freqCorr       int           // frequency error of last hop
	freqCorrection int           // frequencyCorrection (average freqError per transmitter per channel)

	// controll
	initTransmitrs  bool // start an init session to synchronize all defined channels
	handleNxtPacket bool // start preparation for reading next data packet

	// init
	visitCount int // number of different active channels seen during init

	// msg handling
	lastRecMsg string // string of last received raw code

	// test
	testFreq        bool
	startFreq       int
	endFreq         int
	stepFreq        int
	testChannelFreq int
	testNumber      int
)

func init() {
	VERSION := "0.15.2md"
	var (
		tr   int
		mask int
	)
	msgIdToChan = []int{9, 9, 9, 9, 9, 9, 9, 9} // preset with 9 (= undefined)
	receiveWindow = 300                         // in ms

	log.SetFlags(log.Lmicroseconds)
	rand.Seed(time.Now().UnixNano())

	// read program settings
	flag.IntVar(&tr, "tr", 1, "transmitters to listen for: tr1=1, tr2=2, tr3=4, tr4=8, tr5=16 tr6=32, tr7=64, tr8=128")
	flag.IntVar(&ex, "ex", 0, "extra loopPeriod time in msec")
	flag.IntVar(&fc, "fc", 0, "frequency correction in Hz for all channels")
	flag.IntVar(&ppm, "ppm", 0, "frequency correction of rtl dongle in ppm")
	flag.IntVar(&gain, "gain", 0, "tuner gain in tenths of Db")
	// supported gain values: 0, 9, 14, 27, 37, 77, 87, 125, 144, 157, 166, 197, 207,
	// 229, 254, 280, 297, 328, 338, 364, 372, 386, 402, 421, 434, 439, 445, 480, 496.
	flag.IntVar(&maxmissed, "maxmissed", 51, "max missed-packets-in-a-row before new init")
	flag.IntVar(&startFreq, "startfreq", 0, "test")
	flag.IntVar(&endFreq, "endfreq", 0, "test")
	flag.IntVar(&stepFreq, "stepfreq", 0, "test")
	transmitterFreq = flag.String("tf", "EU", "transmitter frequencies: EU, US or NZ")
	undefined = flag.Bool("u", false, "log undefined signals")
	verbose = flag.Bool("v", false, "emit verbose debug messages")
	disableAfc = flag.Bool("noafc", false, "disable any AFC")
	deviceString = flag.String("d", "0", "device serial number or device index")
	graphiteSrv = flag.String("gs", "", "decode packets and send to graphite server")
	graphitePrefix = flag.String("gp", "wx.davis.", "prefix for graphite metrics")

	flag.Parse()
	protocol.Verbose = *verbose

	log.Printf("rtldavis.go VERSION=%s", VERSION)
	// convert tranceiver code to act channels
	mask = 1
	for i := range actChan {
		if tr&mask != 0 {
			actChan[maxChan] = i
			msgIdToChan[i] = maxChan
			maxChan += 1
		}
		mask = mask << 1
	}
	log.Printf("tr=%d fc=%d ppm=%d gain=%d maxmissed=%d ex=%d receiveWindow=%d actChan=%d maxChan=%d", tr, fc, ppm, gain, maxmissed, ex, receiveWindow, actChan[0:maxChan], maxChan)
	log.Printf("undefined=%v verbose=%v disableAfc=%v deviceString=%s", *undefined, *verbose, *disableAfc, *deviceString)

	// Preset loopperiods per id
	idLoopPeriods[0] = 2562500 * time.Microsecond
	for i := 1; i < 8; i++ {
		idLoopPeriods[i] = idLoopPeriods[i-1] + 62500*time.Microsecond
	}

	// check if test
	if startFreq != 0 && endFreq != 0 && stepFreq != 0 {
		log.Printf("TEST: startFreq=%d endFreq=%d stepFreq=%d", startFreq, endFreq, stepFreq)
		testFreq = true
		testChannelFreq = startFreq - stepFreq
	}

}

func main() {
	var sdrIndex int = -1
	p := protocol.NewParser(14, *transmitterFreq)
	p.Cfg.Log()

	fs := p.Cfg.SampleRate

	// First attempt to open the device as a Serial Number
	sdrIndex, _ = rtlsdr.GetIndexBySerial(*deviceString)
	if sdrIndex < 0 {
		indexreturn, err := strconv.Atoi(*deviceString)
		if err != nil {
			log.Printf("Could not parse device\n")
			log.Fatal(err)
		}
		sdrIndex = indexreturn
	}

	dev, err := rtlsdr.Open(0)
	if err != nil {
		log.Fatal(err)
	}

	hop := p.SetHop(0, 0) // start program with first hop frequency
	log.Printf("Hop: %s", hop)
	if err := dev.SetCenterFreq(hop.ChannelFreq + fc); err != nil {
		log.Fatal(err)
	}

	if err := dev.SetSampleRate(fs); err != nil {
		log.Fatal(err)
	}

	// set SetTunerGainMode
	ManualGainMode := true
	if gain == 0 {
		ManualGainMode = false
	}

	if err := dev.SetTunerGainMode(ManualGainMode); err != nil {
		log.Fatal(err)
	}

	if gain != 0 {
		gains, err := dev.GetTunerGains()
		if err != nil {
			log.Printf("GetTunerGains Failed - error: %s\n", err)
		} else if len(gains) > 0 {
			gainInfo := "Supported tuner gain: "
			for i := 0; i < len(gains); i++ {
				gainInfo += fmt.Sprintf("%d Db ", int(gains[i]))
			}
			log.Printf(gainInfo)
		}
		err = dev.SetTunerGain(gain)
		if err != nil {
			log.Printf("SetTunerGain %d gain Failed, error: %s\n", gain, err)
		} else {
			log.Printf("SetTunerGain %d Successful\n", gain)
		}
	}

	tgain := dev.GetTunerGain()
	log.Printf("GetTunerGain: %d Db\n", tgain)

	err = dev.SetFreqCorrection(ppm)
	if err != nil {
		log.Printf("SetFreqCorrection %d ppm Failed, error: %s\n", ppm, err)
	} else {
		log.Printf("SetFreqCorrection %d ppm Successful\n", ppm)
	}

	if err := dev.ResetBuffer(); err != nil {
		log.Fatal(err)
	}

	in, out := io.Pipe()

	go dev.ReadAsync(func(buf []byte) {
		out.Write(buf)
	}, nil, 1, p.Cfg.BlockSize2)

	// Handle frequency hops concurrently since the callback will stall if we
	// stop reading to hop.
	nextHop := make(chan protocol.Hop, 1)
	go func() {
		for hop := range nextHop {
			freqCorr = hop.FreqCorr
			if testFreq {
				freqCorrection = 0
				testChannelFreq = testChannelFreq + stepFreq
				testNumber++
				if testChannelFreq > endFreq {
					endmsg := "Test reached endfreq; test ended"
					log.Fatal(endmsg)
				}
				channelFreq = testChannelFreq
			} else {
				freqCorrection = freqCorr
				log.Printf("Hop: %s", hop)
				actHopChanIdx = hop.ChannelIdx
				channelFreq = hop.ChannelFreq
			}
			if *disableAfc {
				freqCorrection = 0
			}
			if *verbose {
				log.Printf("applied freqCorrection=%d", freqCorrection)
			}

			if err := dev.SetCenterFreq(channelFreq + freqCorrection + fc); err != nil {
				//log.Fatal(err)  // no reason top stop program for one error
				log.Printf("SetCenterFreq: %d error: %s", hop.ChannelFreq, err)
			}
		}
	}()

	var graphiteChan chan []string
	if *graphiteSrv != "" {
		graphiteChan = make(chan []string)
		go sendToGraphite(*graphiteSrv, *graphitePrefix, graphiteChan)
		defer close(graphiteChan)
	} else {
		graphiteSrv = nil
	}

	defer func() {
		in.Close()
		out.Close()
		dev.CancelAsync()
		dev.Close()
		os.Exit(0)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, os.Kill)

	block := make([]byte, p.Cfg.BlockSize2)
	initTransmitrs = true
	maxFreq = p.ChannelCount

	// Set the idLoopPeriods for one full rotation of the pattern + 1.
	loopPeriod = time.Duration(maxFreq+2) * idLoopPeriods[actChan[maxChan-1]]
	loopTimer := time.After(loopPeriod) // loopTimer of highest transmitter
	log.Printf("Init channels: wait max %d seconds for a message of each transmitter", loopPeriod/1000000000)

	for {
		consecutivePacketsMissed := 0
		select {
		case <-sig:
			return
		case <-loopTimer:
			// If the loopTimer has expired one of two things has happened:
			//     1: We've missed a message.
			//     2: We've waited for sync and nothing has happened for a
			//        full cycle of the pattern.

			if testFreq {
				if testNumber > 0 {
					log.Printf("TESTFREQ %d: Frequency %d: NOK", testNumber, testChannelFreq)
				}
				loopPeriod = time.Duration(maxFreq+2) * idLoopPeriods[actChan[maxChan-1]]
				loopTimer = time.After(loopPeriod)
				nextHop <- p.SetHop(0, 0)
			} else {
				if !initTransmitrs {
					// packet missed
					curTime = time.Now().UnixNano()
					consecutivePacketsMissed++
					// forget the handling of this channel; update lastVisitTime as if the packet was received
					chLastVisits[expectedChanPtr] += int64(idLoopPeriods[actChan[expectedChanPtr]])
					// update chLastHops as if the packet was received
					chLastHops[expectedChanPtr] = (chLastHops[expectedChanPtr] + 1) % maxFreq
					// increase missed counters
					chAlarmCnts[expectedChanPtr]++
					chMissPerFreq[actChan[expectedChanPtr]][p.SeqToHop(nextHopChan)]++
					log.Printf("ID:%d packet missed (%d), missed per freq: %d", actChan[expectedChanPtr], chAlarmCnts[expectedChanPtr], chMissPerFreq[actChan[expectedChanPtr]][0:maxFreq])
					if consecutivePacketsMissed >= maxmissed {
						initTransmitrs = true
						consecutivePacketsMissed = 0
					}
				}
				// test again; situation may have changed
				if !initTransmitrs {
					HandleNextHopChannel()
					nextHopChan = chNextHops[expectedChanPtr]
					nextHopTran = actChan[expectedChanPtr]
					loopPeriod = time.Duration(chNextVisits[expectedChanPtr] - curTime + int64(62500*time.Microsecond) + int64((receiveWindow+ex)*1000000))
					loopTimer = time.After(loopPeriod)
					nextHop <- p.SetHop(nextHopChan, nextHopTran)
				} else {
					// reset chLastVisits
					for i := 0; i < maxChan; i++ {
						chLastVisits[i] = 0
					}
					visitCount = 0
					totInit++
					loopPeriod = time.Duration(maxFreq+2) * idLoopPeriods[actChan[maxChan-1]]
					loopTimer = time.After(loopPeriod)
					log.Printf("Init channels: wait max %d seconds for a message of each transmitter", loopPeriod/1000000000)
					nextHop <- p.SetHop(0, 0)
				}
			}

		default:
			in.Read(block)
			handleNxtPacket = false
			for _, msg := range p.Parse(p.Demodulate(block)) {
				if testFreq {
					if testNumber > 0 {
						if msgIdToChan[int(msg.ID)] != 9 {
							log.Printf("TESTFREQ %d: Frequency %d (freqCorr=%d): OK, msg.data: %02X", testNumber, testChannelFreq, freqCorr, msg.Data)
							loopPeriod = time.Duration(maxFreq+2) * idLoopPeriods[actChan[maxChan-1]]
							loopTimer = time.After(loopPeriod)
							nextHop <- p.SetHop(0, 0)
						}
					}
					continue // read next message
				}
				curTime = time.Now().UnixNano()
				//log.Printf("msg.Data: %02X", msg.Data)
				// Keep track of duplicate packets
				seen := string(msg.Data)
				if seen == lastRecMsg {
					log.Printf("duplicate packet: %02X", msg.Data)
					continue // read next message
				}
				lastRecMsg = seen
				// check if msg comes from undefined sensor
				if msgIdToChan[int(msg.ID)] == 9 {
					if *undefined {
						log.Printf("undefined: %02X ID=%d", msg.Data, msg.ID)
					}
					idUndefs[int(msg.ID)]++
					continue // read next message
				}

				// a packet has been received
				consecutivePacketsMissed = 0
				chTotMsgs[msgIdToChan[int(msg.ID)]]++
				chAlarmCnts[msgIdToChan[int(msg.ID)]] = 0 // reset current missed count
				if initTransmitrs {
					if chLastVisits[msgIdToChan[int(msg.ID)]] == 0 {
						visitCount += 1
						chLastVisits[msgIdToChan[int(msg.ID)]] = curTime
						chLastHops[msgIdToChan[int(msg.ID)]] = p.HopToSeq(actHopChanIdx)
						log.Printf("TRANSMITTER %d SEEN", msg.ID)
						if visitCount == maxChan {
							if maxChan > 1 {
								log.Printf("ALL TRANSMITTERS SEEN")
							}
							initTransmitrs = false
							handleNxtPacket = true
						}
					} else {
						chLastVisits[msgIdToChan[int(msg.ID)]] = curTime // update chLastVisits timer
					}
				} else {
					// normal hopping
					chLastHops[msgIdToChan[int(msg.ID)]] = p.HopToSeq(actHopChanIdx)
					chLastVisits[msgIdToChan[int(msg.ID)]] = curTime
					if *undefined {
						log.Printf("%02X %d %d %d %d %d msg.ID=%d undefined:%d",
							msg.Data, chTotMsgs[0], chTotMsgs[1], chTotMsgs[2], chTotMsgs[3], totInit, msg.ID, idUndefs)
					} else {
						log.Printf("%02X %d %d %d %d %d msg.ID=%d",
							msg.Data, chTotMsgs[0], chTotMsgs[1], chTotMsgs[2], chTotMsgs[3], totInit, msg.ID)
					}
					if graphiteSrv != nil {
						// in addition to stdout message, send decoded packet to graphite
						graphiteChan <- protocol.DecodeMsg(msg)
					}
					handleNxtPacket = true
				}
			}
			if handleNxtPacket {
				HandleNextHopChannel()
				nextHopChan = chNextHops[expectedChanPtr]
				nextHopTran = actChan[expectedChanPtr]
				loopPeriod = time.Duration(chNextVisits[expectedChanPtr] - curTime + int64(62500*time.Microsecond) + int64((receiveWindow+ex)*1000000))
				loopTimer = time.After(loopPeriod)
				nextHop <- p.SetHop(nextHopChan, nextHopTran)
			}
		}
	}
}

func convTim(unixTime int64) (t time.Time) {
	return time.Unix(0, unixTime*int64(time.Nanosecond))
}

func HandleNextHopChannel() {
	// calculate chNextVisits times
	for i := 0; i < 8; i++ {
		chNextVisits[i] = 0
		chNextHops[i] = chLastHops[i]
	}
	// check lastVisits; zero values should not happen,
	// but when it does the program will be very busy (c.q. hang)
	for i := 0; i < maxChan; i++ {
		if chLastVisits[i] == 0 {
			log.Printf("ERROR: chLastVisits[%d] should not be zero!", i)
			chLastVisits[i] = curTime // workaround to get further
		}
	}
	for i := 0; i < 8; i++ {
		if msgIdToChan[i] < 9 {
			for chNextVisits[msgIdToChan[i]] = chLastVisits[msgIdToChan[i]]; chNextVisits[msgIdToChan[i]] <= curTime; chNextVisits[msgIdToChan[i]] += int64(idLoopPeriods[actChan[msgIdToChan[i]]]) {
				chNextHops[msgIdToChan[i]] = (chNextHops[msgIdToChan[i]] + 1) % maxFreq
			}
		}
	}
	expectedChanPtr = Min(chNextVisits[0:maxChan])
}

func Min(values []int64) (ptr int) {
	var min int64
	min = values[0]
	for i := 0; i < maxChan; i++ {
		if values[i] < min {
			min = values[i]
			ptr = i
		}
	}
	return ptr
}

func sendToGraphite(server string, prefix string, graphiteChan chan []string) {
	var conn io.Writer
	var err error
	if server == "-" {
		conn = os.Stdout
	} else {
		conn, err = net.Dial("udp", server)
		if err != nil {
			log.Printf("failed to open %s: %s", server, err)
			os.Exit(1)
			return
		}
	}
	log.Printf("connected to %s for graphite output", server)
	for msgs := range graphiteChan {
		/* note that every call to conn.write generates a udp packet; the
		   least we can do is bundle the messages that all arrive
		   together */
		var b bytes.Buffer
		for _, line := range msgs {
			fmt.Fprintf(&b, "%s%s %d\n", prefix, line, time.Now().Unix())
		}
		_, err = b.WriteTo(conn)
		if err != nil {
			log.Printf("failed to write to %s: %s", server, err)
			os.Exit(1)
		}
	}
}
