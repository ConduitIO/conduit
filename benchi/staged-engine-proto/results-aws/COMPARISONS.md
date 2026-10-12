| session | comparison | delta (median to median) | per-round range | A/A floors (x / y) | exceeds both floors |
|---|---|---|---|---|---|
| 01-1x1-bufferedacks | v2-main vs v1 | -33.0% | -33.6% to -32.3% | 2.1% / 1.3% | yes |
| 01-1x1-bufferedacks | v2-proto vs v1 | -20.5% | -21.0% to -20.1% | 0.7% / 1.3% | yes |
| 01-1x1-bufferedacks | v2-proto vs v2-main | +18.7% | +17.9% to +19.2% | 0.7% / 2.1% | yes |
| 01-1x1-bufferedacks | v2-altA vs v1 | +57.7% | +56.2% to +58.5% | 2.0% / 1.3% | yes |
| 01-1x1-bufferedacks | v2-altA vs v2-main | +135.3% | +132.7% to +136.9% | 2.0% / 2.1% | yes |
| 01-1x1-bufferedacks | v2-proto vs v2-altA | -49.6% | -50.2% to -49.3% | 0.7% / 2.0% | yes |
| 02-2x2 | v2-main vs v1 | -5.1% | -5.7% to -5.4% | 4.1% / 5.0% | yes |
| 02-2x2 | v2-proto vs v1 | +84.2% | +81.2% to +85.8% | 3.5% / 5.0% | yes |
| 02-2x2 | v2-proto vs v2-main | +94.2% | +92.1% to +96.9% | 3.5% / 4.1% | yes |
| 03-4x4 | v2-main vs v1 | +43.1% | +41.2% to +44.8% | 1.1% / 2.8% | yes |
| 03-4x4 | v2-proto vs v1 | +101.7% | +98.8% to +104.9% | 2.8% / 2.8% | yes |
| 03-4x4 | v2-proto vs v2-main | +41.0% | +39.0% to +41.5% | 2.8% / 1.1% | yes |
| 04-1x1 | v2-main vs v1 | -24.8% | -25.4% to -23.4% | 0.5% / 1.0% | yes |
| 04-1x1 | v2-proto vs v1 | +3.9% | +2.8% to +5.4% | 1.6% / 1.0% | yes |
| 04-1x1 | v2-proto vs v2-main | +38.0% | +36.7% to +38.1% | 1.6% / 0.5% | yes |
| 04-1x1 | v2-altA vs v1 | +72.2% | +70.3% to +75.2% | 1.6% / 1.0% | yes |
| 04-1x1 | v2-altA vs v2-main | +128.8% | +127.4% to +131.1% | 1.6% / 0.5% | yes |
| 04-1x1 | v2-proto vs v2-altA | -39.7% | -40.3% to -39.6% | 1.6% / 1.6% | yes |
| 05-1x1-streambuf64 | v2-main vs v1 | +5.4% | +5.3% to +5.9% | 1.0% / 0.5% | yes |
| 05-1x1-streambuf64 | v2-proto vs v1 | -1.2% | -1.7% to -1.0% | 0.6% / 0.5% | yes |
| 05-1x1-streambuf64 | v2-proto vs v2-main | -6.2% | -7.2% to -6.0% | 0.6% / 1.0% | yes |
| 06-2x2-streambuf64 | v2-main vs v1 | -57.7% | -58.8% to -57.2% | 1.2% / 5.6% | yes |
| 06-2x2-streambuf64 | v2-proto vs v1 | +101.7% | +95.7% to +106.9% | 2.2% / 5.6% | yes |
| 06-2x2-streambuf64 | v2-proto vs v2-main | +376.6% | +373.6% to +386.3% | 2.2% / 1.2% | yes |
| 07-1x1-noschema | v2-main vs v1 | +62.9% | +61.5% to +64.1% | 1.2% / 1.9% | yes |
| 07-1x1-noschema | v2-proto vs v1 | +52.4% | +51.8% to +53.4% | 1.3% / 1.9% | yes |
| 07-1x1-noschema | v2-proto vs v2-main | -6.5% | -6.5% to -5.6% | 1.3% / 1.2% | yes |
| 08-1x1-lat1ms | v2-main vs v1 | -98.3% | -98.3% to -98.3% | 0.5% / 1.5% | yes |
| 08-1x1-lat1ms | v2-proto vs v1 | -23.8% | -23.8% to -23.3% | 1.3% / 1.5% | yes |
| 08-1x1-lat1ms | v2-proto vs v2-main | +4367.6% | +4341.2% to +4401.1% | 1.3% / 0.5% | yes |
| 09-1x1-destbatch100 | v2-main vs v1 | -99.7% | -99.8% to -99.7% | 1.1% / 1.6% | yes |
| 09-1x1-destbatch100 | v2-proto vs v1 | +25.7% | +25.0% to +26.6% | 0.7% / 1.6% | yes |
| 09-1x1-destbatch100 | v2-proto vs v2-main | +50170.5% | +50146.8% to +50542.3% | 0.7% / 1.1% | yes |
| 10-1x1-srcbatch100 | v2-main vs v1 | +56.2% | +55.4% to +56.3% | 0.8% / 2.8% | yes |
| 10-1x1-srcbatch100 | v2-proto vs v1 | +55.0% | +54.0% to +55.9% | 1.8% / 2.8% | yes |
| 10-1x1-srcbatch100 | v2-proto vs v2-main | -0.8% | -1.0% to +0.0% | 1.8% / 0.8% | NO (per-round sign flips) |
