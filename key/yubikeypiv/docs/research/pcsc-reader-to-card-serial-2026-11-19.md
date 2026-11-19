# Can a PC/SC reader name be mapped to a YubiKey serial without connecting to the card? (2026-11-19)

## Question

The driver now connects to one reader only, the one the operator names (`--reader`, `Options.Reader`; decision D12 C, 2026-11-19). The operator knows the card's serial, not the reader's name. Is there a way to get from a reader name to the serial that reads nothing from the card, so that the driver, or a helper such as `yubikeypiv readers`, could print the pairs?

## When and how

Written on 2026-11-19 from public documentation and source only: pcsc-lite's `readerfactory.c`, the CCID driver's `SCARDGETATTRIB` notes, Ludovic Rousseau's blog post on `SCARD_ATTR_CHANNEL_ID`, and Yubico's documentation of the serial number in the USB descriptor. **Nothing was run against a reader or a YubiKey** for this note: the machine it was written on holds cards that belong to people, so none of the calls below was tried. The claims are what the documents say, and each is marked when it is not confirmed.

## Verdict

**No. There is no mapping that is both safe and reliable without connecting to the card, so the operator names the reader.** The driver lists reader names (that is allowed: `SCardListReaders` connects to no card), puts them in the refusal when there are several, and leaves the choice to the operator. It ships no `readers` subcommand and no name-to-serial table, because no source it could print is trustworthy for a serial.

## What each source gives

| Source | Needs a connection to the card? | What it says about the serial | Verdict |
| --- | --- | --- | --- |
| **The reader name** (`SCardListReaders`) | No | pcsc-lite builds it as `<driver name><optional suffix> <reader index, hex> <slot, hex>`, for example `... 00 00` (`RFSetReaderName` in `readerfactory.c`: format `"%s%s %02X 00"`; the slot digits are overwritten for a reader with several slots). The index is the first unused one among readers of the same driver at enumeration time. **The name carries no serial.** It changes with the order in which the readers appear, so two cards of one model are told apart by an index that the next plug-in can swap. | Cannot map. |
| **The ATR** (`SCardGetStatusChange`, or `SCardStatus`) | No for `SCardGetStatusChange` (it reads the state of the reader, and pcsc-lite has the ATR from when the card was powered up) | The ATR is a property of the card model and firmware family. It has no serial in it as far as the documents read for this note say; this is not confirmed against a YubiKey ATR trace, because none was taken. Two keys of one model have the same ATR, so it cannot tell them apart whatever it holds. | Cannot map. |
| **USB descriptor, `iSerialNumber`** (sysfs `/sys/bus/usb/devices/*/serial`, or udev's `ID_SERIAL_SHORT`) | No (it is read from the kernel) | Yubico documents a setting that makes the serial number visible in the USB descriptor (`SerialNumberUsbVisible`, in the OTP application's settings). So the field holds the serial **only when that setting is on**, and the documents read here do not say what a key ships with or what the setting is on a given card. It is a property of the card's configuration that the operator does not control by the driver. | Possible on a card where it is on, not safe to rely on: a card where it is off gives nothing, and the driver cannot tell "off" from "another key". |
| **USB `bcdDevice`** | No | It reports the firmware version, not a serial (not confirmed against a key here). Every key with the same firmware has the same value. | Cannot map. |
| **`SCARD_ATTR_VENDOR_IFD_SERIAL_NO`** (`SCardGetAttrib`) | Yes: it needs a handle from `SCardConnect`, with `SCARD_SHARE_DIRECT` for a connection to the reader without the card | It is the **reader's** USB serial (the CCID driver reads the USB string descriptor), so it is the same `iSerialNumber` as above, with the same limit. | Not allowed here (a connection), and no better than sysfs. |
| **`SCARD_ATTR_CHANNEL_ID`** (`SCardGetAttrib`) | Yes, as above | For a USB reader the value is `0x0020CCCC`, with the bus number in the high byte of `CCCC` and the USB device address in the low byte (CCID `SCARDGETATTRIB` notes, and Rousseau's blog post of 2020-04). That maps a reader name to a place in `lsusb`, and not to a serial. The device address changes on every plug-in. Rousseau notes that it gives no physical port. | Not allowed here (a connection). Even with it, `lsusb` gives the serial only when `iSerialNumber` is on. |
| **The PIV applet** (`piv.Open`, then `Serial()`) | Yes: it selects the PIV applet | The serial, always. | This is what the old driver did to every reader. It is what D12 C removes for readers other than the named one. |

## Decision

1. The operator names the reader. `Options.Reader` and `--reader` take the exact name that `SCardListReaders` gives, and `yubikeypiv.Readers()` lists the names (no connection).
2. The driver never reads a serial to find a reader. The serial is a check on the card of the reader that was named, after the connection to it.
3. A tool that wants a name-to-serial list has two honest ways: leave only the card in question in a reader while naming it (then there is one reader, and the driver opens it without a name), or connect to each reader on purpose, in a procedure the operator chooses, which is the thing the driver itself does not do any more.
4. A sysfs or udev helper was considered and left out. Where `iSerialNumber` is on, the operator can read it with `udevadm info` or `lsusb -v`, and match it with the reader by the bus and device number, but the driver cannot know whether it is on, so a helper would print a table that is wrong for a card where it is off and look as if it were right. If a future measurement (on a bench card, by the owner) shows that every card a board uses has the descriptor serial on, the helper can be added then.

## Not checked

- Whether a YubiKey 5 ships with the descriptor serial on or off, and what the setting is on the cards in use. The sources read for this note do not say, and no card was asked.
- The ATR of a YubiKey, and whether any byte of it varies from card to card.
- Whether pcscd on Ubuntu 26.04 or Rocky 10 names readers with a suffix from `PCSCLITE_FILTER_EXTEND_READER_NAMES`.

## Sources

- pcsc-lite, `src/readerfactory.c`, `RFSetReaderName` and `RFAddReader` (github.com/LudovicRousseau/PCSC).
- CCID driver, `SCARDGETATTRIB.md` (github.com/LudovicRousseau/CCID), for `SCARD_ATTR_CHANNEL_ID` and the serial attribute.
- Ludovic Rousseau, "SCARD_ATTR_CHANNEL_ID and USB devices", 2020-04, ludovicrousseau.blogspot.com.
- Yubico, `OtpSettings.SetSerialNumberUsbVisible` and `ExtendedFlags.SerialNumberUsbVisible`, docs.yubico.com/yesdk.
