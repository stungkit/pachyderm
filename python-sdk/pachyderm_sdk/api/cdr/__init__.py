# Generated by the protocol buffer compiler.  DO NOT EDIT!
# sources: api/cdr/cdr.proto
# plugin: python-betterproto
# This file has been @generated
from dataclasses import dataclass
from typing import (
    TYPE_CHECKING,
    Dict,
    List,
)

import betterproto
import betterproto.lib.google.protobuf as betterproto_lib_google_protobuf


if TYPE_CHECKING:
    import grpc


class HashAlgo(betterproto.Enum):
    UNKNOWN_HASH = 0
    BLAKE2b_256 = 1


class CipherAlgo(betterproto.Enum):
    UNKNOWN_CIPHER = 0
    CHACHA20 = 1


class CompressAlgo(betterproto.Enum):
    UNKNOWN_COMPRESS = 0
    GZIP = 1


@dataclass(eq=False, repr=False)
class Ref(betterproto.Message):
    http: "Http" = betterproto.message_field(1, group="body")
    """Sources"""

    content_hash: "ContentHash" = betterproto.message_field(2, group="body")
    """Constraints"""

    size_limits: "SizeLimits" = betterproto.message_field(3, group="body")
    cipher: "Cipher" = betterproto.message_field(4, group="body")
    """1:1 Transforms"""

    compress: "Compress" = betterproto.message_field(5, group="body")
    slice: "Slice" = betterproto.message_field(6, group="body")
    concat: "Concat" = betterproto.message_field(7, group="body")
    """Many:1 Transforms"""


@dataclass(eq=False, repr=False)
class Http(betterproto.Message):
    url: str = betterproto.string_field(1)
    headers: Dict[str, str] = betterproto.map_field(
        2, betterproto.TYPE_STRING, betterproto.TYPE_STRING
    )


@dataclass(eq=False, repr=False)
class ContentHash(betterproto.Message):
    """Contraints"""

    inner: "Ref" = betterproto.message_field(1)
    algo: "HashAlgo" = betterproto.enum_field(2)
    hash: bytes = betterproto.bytes_field(3)


@dataclass(eq=False, repr=False)
class SizeLimits(betterproto.Message):
    inner: "Ref" = betterproto.message_field(1)
    min: int = betterproto.int64_field(2)
    max: int = betterproto.int64_field(3)


@dataclass(eq=False, repr=False)
class Cipher(betterproto.Message):
    """1:1 Transforms"""

    inner: "Ref" = betterproto.message_field(1)
    algo: "CipherAlgo" = betterproto.enum_field(2)
    key: bytes = betterproto.bytes_field(3)
    nonce: bytes = betterproto.bytes_field(4)


@dataclass(eq=False, repr=False)
class Compress(betterproto.Message):
    inner: "Ref" = betterproto.message_field(1)
    algo: "CompressAlgo" = betterproto.enum_field(2)


@dataclass(eq=False, repr=False)
class Slice(betterproto.Message):
    """1:1 Transforms"""

    inner: "Ref" = betterproto.message_field(1)
    start: int = betterproto.uint64_field(2)
    end: int = betterproto.uint64_field(3)


@dataclass(eq=False, repr=False)
class Concat(betterproto.Message):
    """Many:1 Transforms"""

    refs: List["Ref"] = betterproto.message_field(1)
