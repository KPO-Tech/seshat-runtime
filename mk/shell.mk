# Which shell runs a recipe, and which tools it finds.
#
# On Windows (PowerShell, cmd or Git Bash) GNU Make does not find Git's sh.exe by itself, and it runs "simple" recipe lines
# without a shell, by CreateProcess, so the coreutils the recipes use (mkdir, rm, find, du, printf, awk...) have to be on
# PATH as well. Git's usr/bin (not bin, which only has sh.exe and bash.exe) ships them all. Both are set here from a bare
# "Git for Windows installed" state. The MinGW-w64 GCC of MSYS2, when it is installed, is put on PATH too: the optional
# native document reading (CGO) needs a C compiler. Elsewhere this file does nothing.

ifeq ($(OS),Windows_NT)
  ifneq ($(wildcard C:/Program\ Files/Git/bin/sh.exe),)
    GIT_ROOT_WIN := C:/Program Files/Git
  else ifneq ($(wildcard C:/Program\ Files\ (x86)/Git/bin/sh.exe),)
    GIT_ROOT_WIN := C:/Program Files (x86)/Git
  else ifneq ($(wildcard $(subst \,/,$(LOCALAPPDATA))/Programs/Git/bin/sh.exe),)
    GIT_ROOT_WIN := $(subst \,/,$(LOCALAPPDATA))/Programs/Git
  endif
  ifneq ($(GIT_ROOT_WIN),)
    SHELL := $(GIT_ROOT_WIN)/bin/sh.exe
    .SHELLFLAGS := -c
    BASH := $(GIT_ROOT_WIN)/bin/bash.exe
    export PATH := $(GIT_ROOT_WIN)/usr/bin;$(PATH)
  endif
  ifneq ($(wildcard C:/msys64/mingw64/bin/gcc.exe),)
    export PATH := C:/msys64/mingw64/bin;$(PATH)
  else ifneq ($(wildcard $(subst \,/,$(LOCALAPPDATA))/Programs/MSYS2/mingw64/bin/gcc.exe),)
    export PATH := $(subst \,/,$(LOCALAPPDATA))/Programs/MSYS2/mingw64/bin;$(PATH)
  endif
endif
BASH ?= bash
