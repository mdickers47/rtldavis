/*
   This module decodes the binary data packets into weather messages.
   Every packet carries one byte for wind speed, one byte for wind
   direction, and one other sensor reading of 10 or more bits, labeled by
   a message identifier.

   The code is adapted from the weewx driver named weewx-rtldavis,
   written by Luc Heijst (also the author of the upstream fork of
   rtldavis):

   https://github.com/lheijst/weewx-rtldavis

   If a bug here is also present in the weewx-rtldavis reference code,
   it should be fixed in both places.  If it is only in this code,
   then it was introduced by me when translating from Python to Go.

   2023-12-19 M. Dickerson <pomonamikey@gmail.com>
*/

package protocol

import (
	"fmt"
	"log"
)

func DecodeMsg(m Message) (obs []string) {
	/* sensor messages arrive with 'channel' numbers, which has no
	   relation to a go chan or an RF frequency. we only understand the
	   ISS ('integrated sensor suite') channel 0, used by Vantage Vue. */
	if m.ID != 0 {
		log.Printf("received message for unsupported channel %d", m.ID)
		return
	}

	windspeed_raw := m.Data[1]
	obs = append(obs, fmt.Sprintf("windspeed_raw %d", windspeed_raw))

	winddir_vue := float64(m.Data[2])*1.40625 + 0.3
	obs = append(obs, fmt.Sprintf("winddir %.2f", winddir_vue))

	/* apply the error correction table that might not even be for the
	   Vantage Vue; it's unclear */
	winddir := CorrectWindspeed(m.Data[1], m.Data[2])
	obs = append(obs, fmt.Sprintf("windspeed %.2f", winddir))

	msg_type := (m.Data[0] >> 4) & 0x0F
	/* most of the time we will use the 10-bit number in this weird place */
	raw := ((int16(m.Data[3]) << 2) + int16(m.Data[4])>>6) & 0x03FF
	switch msg_type {
	case 0x02:
		/* supercap voltage */
		obs = append(obs, fmt.Sprintf("supercap_v_raw %d", raw))
		if raw != 0x03FF {
			obs = append(obs, fmt.Sprintf("supercap_v %0.2f", float32(raw)/300.0))
		}
	case 0x04:
		/* UV radiation */
		obs = append(obs, fmt.Sprintf("uv_raw %d", raw))
		if raw != 0x03FF {
			obs = append(obs, fmt.Sprintf("uv %0.2f", raw/50.0))
		}
	case 0x05:
		/* rain rate */
		time_between_tips_raw := ((int16(m.Data[4]) & 0x30) << 4) + int16(m.Data[3])
		rain_rate := 0.0
		if time_between_tips_raw == 0x03FF {
			rain_rate = 0.0 /* time between tips is infinity => no rain */
		} else if m.Data[4]&0x40 == 0 {
			/* "heavy rain", time-between-tips is scaled up by 4 bits */
			/* TODO: we are assuming the 0.01 inch (=0.254mm) size bucket */
			rain_rate = 3600.0 / (float64(time_between_tips_raw) / 16.0) * 0.254
		} else {
			/* "light rain" formula */
			rain_rate = 3600.0 / float64(time_between_tips_raw) * 0.254
		}
		obs = append(obs, fmt.Sprintf("rain_rate_mmh %.2f", rain_rate))
	case 0x06:
		/* solar radiation */
		if raw < 0x03fE {
			sr := float64(raw) * 1.757936
			obs = append(obs, fmt.Sprintf("solar_radiation %.2f", sr))
		}
	case 0x07:
		/* solar panel output */
		if raw != 0x03FF {
			obs = append(obs, fmt.Sprintf("solar_panel_v %.2f", float32(raw)/300.0))
		}
	case 0x08:
		/* temperature */
		raw = (int16(m.Data[3]) << 4) + (int16(m.Data[4]) >> 4)
		if raw != 0x0FFC {
			if m.Data[4]&0x08 != 0 {
				/* digital sensor */
				obs = append(obs, fmt.Sprintf("temp %.2f", float32(raw)/10.0))
			} else {
				/* TODO: thermistor */
				log.Printf("can't interpret analog temperature sensor reading %d", raw)
				obs = append(obs, fmt.Sprintf("temp_raw %d", raw))
			}
		}
	case 0x09:
		/* 10-min average wind gust */
		gust_raw := m.Data[3]
		gust_index := m.Data[5] >> 4 /* ??? */
		obs = append(obs, fmt.Sprintf("gust_mph %d", gust_raw))
		obs = append(obs, fmt.Sprintf("gust_index %d", gust_index))
	case 0x0a:
		/* humidity */
		raw = (int16(m.Data[4]>>4) << 8) + int16(m.Data[3])
		if raw != 0 {
			if m.Data[4]&0x08 != 0 {
				/* digital sensor */
				obs = append(obs, fmt.Sprintf("humidity %.2f", float32(raw)/10.0))
			} else {
				/* TODO: two other types of digital sensor and one analog */
				log.Printf("can't interpret humidity sensor reading %d", raw)
				obs = append(obs, fmt.Sprintf("humidity_raw %d", raw))
			}
		}
	case 0x0e:
		/* rain */
		raw = int16(m.Data[3]) /* "rain count raw"?? */
		/* ignore the high bit because apparently some counters wrap at
		   128 and others at 256, and it doesn't matter */
		if raw != 0x80 {
			raw &= 0x7f
			obs = append(obs, fmt.Sprintf("rain_count %d", raw))
		}
	default:
		log.Printf("Unknown data packet type 0x%02x: %02x", m.ID, m.Data)
	}
	return
}

/* this table is from github.com/lheijst/weewx-rtldavis.  It says the
   offsets were determined by feeding constructed packets to a Davis
   Envoy and reading the serial output packets.

   Columns are wind direction, rows are windspeed.
*/

var wind_offsets = [55][35]int16{
	{0, 1, 4, 8, 12, 16, 20, 24, 28, 32, 36, 40, 44, 48, 52, 56, 60, 64, 68, 72, 76, 80, 84, 88, 92, 96, 100, 104, 108, 112, 116, 120, 124, 127, 128},
	{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	{2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	{3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 0, 0, 0},
	{4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 0, 0, 0},
	{5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 0, 0},
	{6, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 1, 1, 1, 1, 1, 0, 0},
	{7, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 1, 0, 0},
	{8, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 1, 0, 0},
	{9, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 1, 0, 0},
	{10, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 0, 0},
	{11, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 0, 0},
	{12, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 0, 0},
	{13, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 3, 3, 1, 0, 0},
	{14, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 3, 3, 1, 0, 0},
	{15, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 3, 3, 1, 0, 0},
	{16, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 3, 3, 1, 0, 0},
	{17, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 3, 3, 1, 0, 0},
	{18, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 3, 3, 1, 0, 0},
	{19, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 3, 4, 4, 1, 0, 0},
	{20, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 1, 3, 4, 4, 2, 0, 0},
	{21, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 1, 3, 4, 4, 2, 0, 0},
	{22, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 3, 4, 4, 2, 0, 0},
	{23, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 3, 4, 4, 2, 0, 0},
	{24, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 2, 3, 4, 4, 2, 0, 0},
	{25, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 2, 3, 4, 4, 2, 0, 0},
	{26, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 2, 3, 5, 4, 2, 0, 0},
	{27, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 2, 3, 5, 5, 2, 0, 0},
	{28, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 2, 3, 5, 5, 2, 0, 0},
	{29, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 2, 3, 5, 5, 2, 0, 0},
	{30, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 2, 3, 5, 5, 2, 0, 0},
	{35, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 2, 4, 6, 5, 2, 0, -1},
	{40, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 2, 4, 6, 6, 2, 0, -1},
	{45, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 2, 4, 7, 6, 2, -1, -1},
	{50, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 2, 5, 7, 7, 2, -1, -2},
	{55, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 2, 5, 8, 7, 2, -1, -2},
	{60, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 2, 5, 8, 8, 2, -1, -2},
	{65, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 2, 5, 9, 8, 2, -2, -3},
	{70, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 0, 2, 5, 9, 9, 2, -2, -3},
	{75, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 0, 2, 6, 10, 9, 2, -2, -3},
	{80, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 0, 2, 6, 10, 10, 2, -2, -3},
	{85, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 0, 2, 7, 11, 11, 2, -3, -4},
	{90, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 2, 7, 12, 11, 2, -3, -4},
	{95, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 2, 2, 2, 3, 2, 2, 2, 1, 1, 1, 1, 2, 7, 12, 12, 3, -3, -4},
	{100, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 2, 2, 3, 3, 2, 2, 2, 1, 1, 1, 1, 2, 8, 13, 12, 3, -3, -4},
	{105, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 3, 3, 3, 3, 3, 2, 2, 2, 1, 1, 1, 2, 8, 13, 13, 3, -3, -4},
	{110, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 3, 3, 3, 3, 3, 2, 2, 2, 1, 1, 1, 2, 8, 14, 14, 3, -3, -5},
	{115, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 2, 3, 3, 3, 3, 3, 2, 2, 2, 1, 1, 1, 2, 9, 15, 14, 3, -3, -5},
	{120, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 2, 3, 3, 3, 3, 3, 2, 2, 2, 1, 1, 1, 3, 9, 15, 15, 3, -4, -5},
	{125, 1, 1, 2, 1, 1, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 3, 3, 3, 3, 3, 2, 2, 1, 1, 1, 3, 10, 16, 16, 3, -4, -5},
	{130, 1, 1, 2, 1, 1, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 3, 3, 3, 3, 3, 2, 2, 2, 1, 1, 3, 10, 17, 16, 3, -4, -6},
	{135, 1, 2, 2, 1, 1, 0, 0, 0, -1, 0, 0, 1, 1, 2, 2, 3, 3, 3, 3, 4, 3, 3, 2, 2, 2, 1, 1, 3, 10, 17, 17, 4, -4, -6},
	{140, 1, 2, 2, 1, 1, 0, 0, 0, -1, 0, 0, 1, 1, 2, 2, 3, 3, 3, 4, 4, 3, 3, 2, 2, 2, 1, 1, 3, 11, 18, 17, 4, -4, -6},
	{145, 2, 2, 2, 1, 1, 0, 0, 0, -1, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 4, 3, 3, 3, 2, 2, 1, 1, 3, 11, 19, 18, 4, -4, -6},
	{150, 2, 2, 2, 1, 1, 0, 0, -1, -1, 0, 0, 1, 1, 2, 3, 3, 4, 4, 4, 4, 4, 3, 3, 2, 2, 1, 1, 3, 12, 19, 19, 4, -4, -6},
}

func CorrectWindspeed(s byte, d byte) float32 {
	/* speed is mph, direction is degrees where 0 = north */

	/* table is treated as having east-west symmetry */
	if d > 127 {
		d = byte(256 - int16(d))
	}

	/* on each axis, find the first term greater than x */
	row, col := 1, 1
	for wind_offsets[row][0] <= int16(s) && row < 54 {
		row++
	}
	for wind_offsets[0][col] <= int16(d) && col < 34 {
		col++
	}

	corr := float32(wind_offsets[row][col])
	var delta_s, delta_d float32

	/* we are talking about fractions of a mph at this point, but
	   nevertheless we soldier on and do a bilinear interpretation to
	   approximate the corrections where the table does not have an
	   exact match */

	if col > 1 && wind_offsets[0][col] != int16(d) {
		d_selected := float32(wind_offsets[0][col])
		d_prev := float32(wind_offsets[0][col-1])
		corr_prev := float32(wind_offsets[row][col-1])
		delta_d = (corr_prev - corr) +
			(float32(d)-d_prev)/(d_selected-d_prev)*(corr-corr_prev)
	}

	if row > 1 && wind_offsets[row][0] != int16(s) {
		s_selected := float32(wind_offsets[row][0])
		s_prev := float32(wind_offsets[row-1][0])
		corr_prev := float32(wind_offsets[row-1][col])
		delta_s = (corr_prev - corr) +
			(float32(s)-s_prev)/(s_selected-s_prev)*(corr-corr_prev)
	}

	return float32(s) + corr + delta_d + delta_s
}
