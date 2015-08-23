# Change Log
All notable changes to this project will be documented in this file.
This project adheres to [Semantic Versioning](http://semver.org/).

## [0.0.4] - 2015-08-22

### New Features
- Run conservatively: Facts will only be obtained when requested

### Removed Features
- Text Templates: The ability to format results as a text template has been removed.

### Changed
- The internal structure of the code has been modified to accommodate more than just the Linux platform.

## [0.0.3] - 2015-08-17

### New Features
- Debug mode: only errors are printed in debug mode
- Specify a fact to query on the command-line

### New Facts
- Processor
- DMI
- Block Devices

### Changed
- Add LSB Codename facts to OSRelease
- Add timezone and offset to Date
- Fixed load average calculation

## [0.0.2] - 2015-04-30

### Changed
- Add Ip4Addresses and Ip6Addresses facts to interfaces
