#!/bin/bash
# macOS ships rsync 2.6.9 (Apple never shipped a GPLv3 rsync), which doesn't
# know --info=progress2 (that's 3.1.0+) -- -P (--partial --progress) is the
# per-file progress display this old version actually supports.
rsync -avzP server/data/*.bolt new.zooloo.org:/srv/birdquiz.byteboa.org/
