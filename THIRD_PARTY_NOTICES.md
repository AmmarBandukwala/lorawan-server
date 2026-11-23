# Third-party notices

A small part of this repository was written by reading, and in a few cases copying
test data from, an MIT-licensed LoRaWAN implementation. MIT requires the copyright
notice and permission notice to accompany those portions, so they are reproduced
here rather than scattered through the source.

> **Why this file exists now that the repository has a license:** this project is
> MIT-licensed, which grants permission for the code *this repository* wrote. It does
> not discharge the separate obligation to carry the upstream MIT notice with the
> portions reproduced below, and those portions remain under their own copyright.
> The two licenses are the same terms, so there is no conflict to resolve — but the
> attribution below still has to travel with the code.

## What was used, and where it landed

| Upstream | Where it influenced this code |
| --- | --- |
| `lrwn` test vectors (`lrwn/src/keys.rs`) | Key-derivation vectors in `ns/internal/lorawan/keys_vectors_test.go` |
| `lrwn` frame tests (`lrwn/src/phy_payload.rs`) | Frame encode/decode vectors in `ns/internal/lorawan/phy_payload_vectors_test.go` and the OTAA round-trip test |
| Counter reconstruction (`get_full_f_cnt_up`, `test_get_full_f_cnt_up`) | `ns/internal/lorawan/fcnt.go` and its assertion table in `fcnt_vectors_test.go` |
| Default ADR algorithm (`chirpstack/src/adr/default.rs`) | Parameterisation of `ns/internal/engine/adr.go`: 20-sample window, 3 dB per step, 10 dB installation margin, the loss-band `NbTrans` table, and the partial-window guard |
| Required-SNR-per-spreading-factor table | `requiredSNRForSF` in `ns/internal/engine/adr.go` |
| Regional parameter tables (`lrwn/src/region/`) | Cross-check for `ns/internal/region/`; the underlying values are RP002 1.0.3, which is the normative source |

Everything else — the engine's join/uplink/downlink pipeline, the Semtech UDP
transport, duty-cycle and time-on-air enforcement, the channel-mask handling, the
storage layer, the console API and the dashboard — was written for this project
against the LoRaWAN 1.0.3, RP002 1.0.3 and RFC 4493 documents.

Where a specification left a case genuinely open, the behaviour chosen is the one
deployed devices and gateways expect, so that ordinary hardware works without
per-vendor special cases.

## MIT license text

The MIT License (MIT)

Copyright (c) 2022 Orne Brocaar

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
