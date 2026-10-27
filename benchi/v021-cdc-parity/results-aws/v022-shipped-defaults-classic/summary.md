| arm | n | median rate/s | min-max rate/s | spread (max-min)/median | median lag start -> end (events) |
| --- | --- | --- | --- | --- | --- |
| conduit | 5 | 19,779 | 19,765 - 19,780 | 0.1% | 20,000 -> 13,430 |
| conduit-aa | 5 | 19,780 | 19,773 - 19,783 | 0.1% | 20,406 -> 16,000 |
| conduit-kafka | 5 | 5,318 | 5,293 - 5,369 | 1.4% | 295,044 -> 1,203,535 |
| conduit-kafka-aa | 5 | 5,315 | 5,297 - 5,359 | 1.2% | 294,467 -> 1,185,153 |
| conduit-pg | 5 | 3,390 | 3,383 - 3,409 | 0.8% | 131,590 -> 635,559 |
| conduit-pg-aa | 5 | 3,392 | 3,375 - 3,405 | 0.9% | 131,333 -> 644,354 |
| conduit-s3 | 5 | 17,256 | 17,243 - 17,307 | 0.4% | 43,000 -> 225,000 |
| conduit-s3-aa | 5 | 17,248 | 17,227 - 17,336 | 0.6% | 39,000 -> 227,000 |
| connect | 5 | 19,792 | 19,726 - 19,859 | 0.7% | 348,584 -> 341,462 |
| connect-kafka | 5 | 19,772 | 19,734 - 19,795 | 0.3% | 2,888 -> 11,315 |
| connect-pg | 5 | 11,856 | 11,797 - 11,868 | 0.6% | -2,014 -> -1,168 |
| connect-s3 | 5 | 17,595 | 17,497 - 17,672 | 1.0% | 39,000 -> 194,000 |

| pair | median ratio (conduit / connect) | per-round ratios |
| --- | --- | --- |
| conduit vs connect | 0.999 | 0.998, 1.002, 0.999, 0.996, 1.001 |
| conduit-aa vs connect | 0.999 | 0.998, 1.003, 0.999, 0.996, 1.001 |
| conduit-pg vs connect-pg | 0.287 | 0.286, 0.287, 0.287, 0.286, 0.287 |
| conduit-pg-aa vs connect-pg | 0.287 | 0.287, 0.286, 0.288, 0.287, 0.286 |
| conduit-s3 vs connect-s3 | 0.984 | 0.988, 0.984, 0.976, 0.979, 0.984 |
| conduit-s3-aa vs connect-s3 | 0.981 | 0.986, 0.990, 0.975, 0.981, 0.980 |
| conduit-kafka vs connect-kafka | 0.269 | 0.271, 0.268, 0.269, 0.269, 0.270 |
| conduit-kafka-aa vs connect-kafka | 0.269 | 0.269, 0.268, 0.269, 0.271, 0.269 |

A/A (conduit vs conduit-aa): per-round |delta| median 0.03%, max 0.07%
A/A (conduit-pg vs conduit-pg-aa): per-round |delta| median 0.49%, max 0.59%
A/A (conduit-s3 vs conduit-s3-aa): per-round |delta| median 0.27%, max 0.54%
A/A (conduit-kafka vs conduit-kafka-aa): per-round |delta| median 0.41%, max 0.76%
