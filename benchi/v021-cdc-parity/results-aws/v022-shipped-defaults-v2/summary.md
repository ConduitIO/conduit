| arm | n | median rate/s | min-max rate/s | spread (max-min)/median | median lag start -> end (events) |
| --- | --- | --- | --- | --- | --- |
| conduit | 5 | 19,778 | 19,765 - 19,791 | 0.1% | 21,000 -> 24,000 |
| conduit-aa | 5 | 19,783 | 19,774 - 19,793 | 0.1% | 21,000 -> 25,000 |
| conduit-kafka | 5 | 19,772 | 19,755 - 19,787 | 0.2% | 5,210 -> -4,486 |
| conduit-kafka-aa | 5 | 19,768 | 19,761 - 19,776 | 0.1% | -8,000 -> -2,700 |
| conduit-pg | 5 | 11,835 | 11,834 - 11,846 | 0.1% | -7,088 -> 1,730 |
| conduit-pg-aa | 5 | 11,834 | 11,833 - 11,844 | 0.1% | -7,121 -> -6,517 |
| conduit-s3 | 5 | 17,358 | 17,220 - 18,544 | 7.6% | 41,000 -> 210,000 |
| conduit-s3-aa | 5 | 17,370 | 17,235 - 17,417 | 1.0% | 42,000 -> 223,000 |
| connect | 5 | 19,736 | 19,687 - 19,844 | 0.8% | 332,880 -> 337,358 |
| connect-kafka | 5 | 19,785 | 19,632 - 19,801 | 0.9% | 7,008 -> 2,568 |
| connect-pg | 5 | 11,825 | 11,792 - 11,887 | 0.8% | -2,014 -> 591 |
| connect-s3 | 5 | 17,605 | 17,564 - 17,660 | 0.5% | 37,000 -> 197,000 |

| pair | median ratio (conduit / connect) | per-round ratios |
| --- | --- | --- |
| conduit vs connect | 1.002 | 1.002, 1.003, 0.999, 0.997, 1.004 |
| conduit-aa vs connect | 1.002 | 1.002, 1.004, 1.000, 0.997, 1.005 |
| conduit-pg vs connect-pg | 1.001 | 1.001, 1.003, 1.000, 0.996, 1.005 |
| conduit-pg-aa vs connect-pg | 1.001 | 1.001, 1.003, 1.000, 0.996, 1.004 |
| conduit-s3 vs connect-s3 | 0.985 | 1.056, 0.977, 0.985, 0.985, 0.986 |
| conduit-s3-aa vs connect-s3 | 0.987 | 0.987, 0.988, 0.984, 0.989, 0.979 |
| conduit-kafka vs connect-kafka | 1.000 | 1.007, 1.000, 1.000, 0.998, 1.006 |
| conduit-kafka-aa vs connect-kafka | 0.999 | 1.007, 0.999, 0.999, 0.999, 1.005 |

A/A (conduit vs conduit-aa): per-round |delta| median 0.07%, max 0.10%
A/A (conduit-pg vs conduit-pg-aa): per-round |delta| median 0.01%, max 0.11%
A/A (conduit-s3 vs conduit-s3-aa): per-round |delta| median 0.71%, max 7.00%
A/A (conduit-kafka vs conduit-kafka-aa): per-round |delta| median 0.09%, max 0.11%
