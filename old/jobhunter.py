#!/usr/bin/env python3
import sys
import os

# Add the current directory to sys.path so we can import the jobhunter package
sys.path.append(os.path.dirname(os.path.abspath(__file__)))

from jobhunter.main import main
import asyncio

if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        pass
