import os
import sys

from evdev import UInput, ecodes

STATES = {"open": 0, "closed": 1}

fifo = sys.argv[1]
with UInput({ecodes.EV_SW: [ecodes.SW_LID]}, name="lys-wm test lid switch") as lid:
    os.mkfifo(fifo)
    while True:
        with open(fifo) as commands:
            for command in commands:
                lid.write(ecodes.EV_SW, ecodes.SW_LID, STATES[command.strip()])
                lid.syn()
