/*
rtldavis, an rtl-sdr receiver for Davis Instruments weather stations.
Copyright (C) 2015 Douglas Hall

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

Modified by Luc Heijst - March 2019
Version: 0.13 - March 2019

	Added: EU-frequencies
	Removed: frequency correction
	Removed: parsing

Version: 0.14 - May 2020

	Added: NZ-frequency handling
	Changed: storage of freqError for the right channer and transmitter

Version: 0.15 - May 2020

	Changed: an improved calculation of freqError (thanks to Steve Wormley)
*/
package protocol

import (
	"fmt"
	"log"
	"math"
	"math/rand"

	"github.com/lheijst/rtldavis/crc"
	"github.com/lheijst/rtldavis/dsp"
)

var Verbose bool

func NewPacketConfig(symbolLength int) (cfg dsp.PacketConfig) {
	return dsp.NewPacketConfig(
		19200,
		14,
		16,
		80,
		"1100101110001001",
	)
}

const maxTrCh = 10
const maxTr = 8
const maxCh = 51

type Parser struct {
	dsp.Demodulator
	crc.CRC
	Cfg             dsp.PacketConfig
	ChannelCount    int
	channels        []int
	hopIdx          int
	hopPattern      []int
	reverseHopPatrn []int
	freqCorr        int
	transmitter     int
	chfreq          int
	freqerrTrChList [maxTr][maxCh][maxTrCh]int
	freqerrTrChSum  [maxTr][maxCh]int
	freqerrTrChPtr  [maxTr][maxCh]int
	maxTrChList     int
	factor          float32
}

func NewParser(symbolLength int, tf string) (p Parser) {
	p.Cfg = NewPacketConfig(symbolLength)
	p.Demodulator = dsp.NewDemodulator(&p.Cfg)
	p.CRC = crc.NewCRC("CCITT-16", 0, 0x1021, 0)
	p.maxTrChList = maxTrCh

	if tf == "EU" {
		p.channels = []int{
			868077250, 868197250, 868317250, 868437250, 868557250, // EU test 20190324
		}
		p.ChannelCount = len(p.channels)
		p.hopIdx = rand.Intn(p.ChannelCount)
		p.hopPattern = []int{
			0, 2, 4, 1, 3,
		}
		p.reverseHopPatrn = []int{
			0, 3, 1, 4, 2,
		}
	} else {
		if tf == "NZ" {
			p.channels = []int{
				// Digitally captured from CC1101 radio.
				921070709, 921207977, 921345642, 921482910, 921620178, 921757446,
				921894714, 922031586, 922168854, 922306519, 922443787, 922581055,
				922718323, 922855591, 922992859, 923130127, 923267395, 923404266,
				923541931, 923678802, 923816467, 923953735, 924091003, 924228271,
				924365143, 924502411, 924638885, 924776550, 924913818, 925051086,
				925188354, 925325623, 925462494, 925600159, 925737030, 925874695,
				926011963, 926149231, 926286499, 926423370, 926561035, 926697906,
				926835571, 926972839, 927110107, 927246979, 927384644, 927521515,
				927658783, 927796448, 927933716,
			}
		} else {
			p.channels = []int{
				// Thanks to Paul Anderson and Rich T for testing the US frequencies
				902419338, 902921088, 903422839, 903924589, 904426340, 904928090, // US freq per 20190326
				905429841, 905931591, 906433342, 906935092, 907436843, 907938593,
				908440344, 908942094, 909443845, 909945595, 910447346, 910949096,
				911450847, 911952597, 912454348, 912956099, 913457849, 913959599,
				914461350, 914963100, 915464850, 915966601, 916468351, 916970102,
				917471852, 917973603, 918475353, 918977104, 919478854, 919980605,
				920482355, 920984106, 921485856, 921987607, 922489357, 922991108,
				923492858, 923994609, 924496359, 924998110, 925499860, 926001611,
				926503361, 927005112, 927506862,
			}
		}
		// Both NZ and US use the same hop sequence
		p.ChannelCount = len(p.channels)
		p.hopIdx = rand.Intn(p.ChannelCount)
		p.hopPattern = []int{
			0, 19, 41, 25, 8, 47, 32, 13, 36, 22, 3, 29, 44, 16, 5, 27, 38,
			10, 49, 21, 2, 30, 42, 14, 48, 7, 24, 34, 45, 1, 17, 39, 26, 9,
			31, 50, 37, 12, 20, 33, 4, 43, 28, 15, 35, 6, 40, 11, 23, 46, 18,
		}
		p.reverseHopPatrn = []int{
			0, 29, 20, 10, 40, 14, 45, 25, 4, 33, 17, 47, 37, 7, 23, 43, 13,
			30, 50, 1, 38, 19, 9, 48, 26, 3, 32, 15, 42, 11, 21, 34, 6, 39,
			27, 44, 8, 36, 16, 31, 46, 2, 22, 41, 12, 28, 49, 5, 24, 18, 35,
		}
	}
	return
}

type Hop struct {
	ChannelIdx  int
	ChannelFreq int
	FreqCorr    int
	Transmitter int
}

func (h Hop) String() string {
	// Note program rtldavis.py parse on text 'FreqError' and not on text 'FreqCorr'
	return fmt.Sprintf("{ChannelIdx:%d ChannelFreq:%d FreqError:%d Transmitter:%d}",
		h.ChannelIdx, h.ChannelFreq, h.FreqCorr, h.Transmitter,
	)
}

func (p *Parser) hop() (h Hop) {
	h.ChannelIdx = p.hopPattern[p.hopIdx]
	h.ChannelFreq = p.channels[h.ChannelIdx]
	h.FreqCorr = p.freqCorr
	h.Transmitter = p.transmitter
	return h
}

// Set the pattern index and return the new channel's parameters.
func (p *Parser) SetHop(n int, tr int) Hop {
	// The value of p.factor is experimental; the goal is a steady list of frecErrors over time.
	p.factor = (float32(p.maxTrChList/2) + float32(0.5)) * float32(2.0)
	p.hopIdx = n % p.ChannelCount
	ch := p.hopPattern[p.hopIdx]
	idx := p.freqerrTrChPtr[tr][ch]
	// The applied frequency of round (n) is based upon the applied frequency correction
	// of round (n-1) and the measured freqError of round (n-1).
	// In fact we have to adjust the original frequency with the sum of all freqCorrections
	// of round 1 to n.
	// For practical reasons we only use the history of the freqErrors in the freqerrTrChList.
	// The elder the freqErrors, the less influence they have in the applied freqCorreection.

	p.freqCorr = 0
	for i := 0; i < p.maxTrChList; i++ {
		p.freqCorr = p.freqCorr + (p.freqerrTrChList[tr][ch][idx] * (i + 1) / p.maxTrChList)
		idx = (idx + 1) % p.maxTrChList
	}
	p.freqCorr = int(float32(p.freqCorr) / p.factor)
	// set index to last freqError
	idx = (idx + p.maxTrChList - 1) % p.maxTrChList
	if Verbose {
		log.Printf("tr=%d ch=%d freqCorr=%d lastFreqError=%d, freqerrTrChList=%d",
			tr, ch, p.freqCorr, p.freqerrTrChList[tr][ch][idx], p.freqerrTrChList[tr][ch])
	}
	p.transmitter = tr
	return p.hop()
}

// Find sequence-id with hop-id
func (p *Parser) HopToSeq(n int) int {
	return p.reverseHopPatrn[n%p.ChannelCount]
}

// Find hop-id with sequence-id
func (p *Parser) SeqToHop(n int) int {
	return p.hopPattern[n%p.ChannelCount]
}

// Given a list of packets, check them for validity and ignore duplicates,
// return a list of parsed messages.
func (p *Parser) Parse(pkts []dsp.Packet) (msgs []Message) {
	seen := make(map[string]bool)

	for _, pkt := range pkts {
		// Bit order over-the-air is reversed.
		for idx, b := range pkt.Data {
			pkt.Data[idx] = SwapBitOrder(b)
		}
		// Keep track of duplicate packets.
		s := string(pkt.Data)
		if seen[s] {
			continue
		}
		seen[s] = true

		// If the checksum fails, bail.
		if p.Checksum(pkt.Data[2:]) != 0 {
			continue
		}
		// Thanks to Steve Wormley for an improved calculation of freqError.
		// Look at the packet's preamble to determine frequency error between
		// transmitter and receiver.
		// It should have equal ones and zeros so we should average out to 0
		// Have to stride this at the same as symbol length
		lower := pkt.Idx + 0*p.Cfg.SymbolLength
		upper := pkt.Idx + 16*p.Cfg.SymbolLength
		tail := p.Demodulator.Discriminated[lower:upper]
		stride := lower % p.Cfg.SymbolLength
		count := 0
		var mean float64
		var discrim [16]float64
		for i, sample := range tail {
			if i%p.Cfg.SymbolLength == stride {
				mean += sample
				discrim[count] = sample
				count++
			}
		}
		mean /= float64(count)
		if Verbose {
			log.Printf("m1: %f l: %d c: %d x: %.2f", mean, len(tail), count, discrim)
		}

		// The preamble is a set of 0 and 1 symbols, equal in number. The driminator's output is
		// measured in radians.
		freqerr := -int((mean * float64(p.Cfg.SampleRate)) / (2 * math.Pi))
		msg := NewMessage(pkt)
		msgs = append(msgs, msg)
		// Per transmitter and per channel we have a list of p.maxTrChList frequency errors
		// The average value of the frequencu erreors in the list is used for the frequency correction.
		tr := int(msg.ID)
		ch := p.hopPattern[p.hopIdx]
		p.freqerrTrChList[tr][ch][p.freqerrTrChPtr[tr][ch]] = freqerr
		p.freqerrTrChPtr[tr][ch] = (p.freqerrTrChPtr[tr][ch] + 1) % p.maxTrChList
	}
	return
}

type Message struct {
	dsp.Packet
	ID         byte
	BatteryLow bool
}

func NewMessage(pkt dsp.Packet) (m Message) {
	m.Idx = pkt.Idx
	m.Data = make([]byte, len(pkt.Data)-2)
	copy(m.Data, pkt.Data[2:])
	m.ID = m.Data[0] & 0x7
	m.BatteryLow = ((m.Data[0]>>3)&0x01 == 1)
	return m
}

func (m Message) String() string {
	return fmt.Sprintf("{ID:%d}", m.ID)
}

func SwapBitOrder(b byte) byte {
	b = ((b & 0xF0) >> 4) | ((b & 0x0F) << 4)
	b = ((b & 0xCC) >> 2) | ((b & 0x33) << 2)
	b = ((b & 0xAA) >> 1) | ((b & 0x55) << 1)
	return b
}
