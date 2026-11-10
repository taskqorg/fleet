#![allow(clippy::redundant_closure_call)]
#![allow(clippy::needless_lifetimes)]
#![allow(clippy::match_single_binding)]
#![allow(clippy::clone_on_copy)]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug)]
pub struct Item {
    #[serde(default, skip_serializing_if = "::std::option::Option::is_none")]
    pub command: ::std::option::Option<::std::string::String>,
    #[serde(default, skip_serializing_if = "::std::option::Option::is_none")]
    pub status: ::std::option::Option<::std::string::String>,
    #[serde(default, skip_serializing_if = "::std::option::Option::is_none")]
    pub text: ::std::option::Option<::std::string::String>,
    #[serde(rename = "type")]
    pub type_: ::std::string::String,
}
impl Item {
    pub fn builder() -> builder::Item {
        Default::default()
    }
}
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug)]
pub struct ItemCompleted {
    pub data: ItemCompletedData,
    pub seq: u64,
    #[serde(rename = "type")]
    pub type_: ItemCompletedType,
}
impl ItemCompleted {
    pub fn builder() -> builder::ItemCompleted {
        Default::default()
    }
}
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug)]
pub struct ItemCompletedData {
    pub item: Item,
}
impl ItemCompletedData {
    pub fn builder() -> builder::ItemCompletedData {
        Default::default()
    }
}
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum ItemCompletedType {
    #[serde(rename = "item.completed")]
    ItemCompleted,
}
impl ::std::fmt::Display for ItemCompletedType {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::ItemCompleted => f.write_str("item.completed"),
        }
    }
}
impl ::std::str::FromStr for ItemCompletedType {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "item.completed" => Ok(Self::ItemCompleted),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for ItemCompletedType {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<&::std::string::String> for ItemCompletedType {
    type Error = self::error::ConversionError;
    fn try_from(
        value: &::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for ItemCompletedType {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug)]
pub struct OutboxFailed {
    pub data: OutboxFailedData,
    pub seq: u64,
    #[serde(rename = "type")]
    pub type_: OutboxFailedType,
}
impl OutboxFailed {
    pub fn builder() -> builder::OutboxFailed {
        Default::default()
    }
}
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug)]
pub struct OutboxFailedData {
    pub reason: ::std::string::String,
    pub route: ::std::string::String,
    pub status: i64,
}
impl OutboxFailedData {
    pub fn builder() -> builder::OutboxFailedData {
        Default::default()
    }
}
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum OutboxFailedType {
    #[serde(rename = "outbox.failed")]
    OutboxFailed,
}
impl ::std::fmt::Display for OutboxFailedType {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::OutboxFailed => f.write_str("outbox.failed"),
        }
    }
}
impl ::std::str::FromStr for OutboxFailedType {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "outbox.failed" => Ok(Self::OutboxFailed),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for OutboxFailedType {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<&::std::string::String> for OutboxFailedType {
    type Error = self::error::ConversionError;
    fn try_from(
        value: &::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for OutboxFailedType {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug)]
#[serde(untagged)]
pub enum TranscriptEvent {
    ItemCompleted(ItemCompleted),
    TurnCompleted(TurnCompleted),
    OutboxFailed(OutboxFailed),
}
impl ::std::convert::From<ItemCompleted> for TranscriptEvent {
    fn from(value: ItemCompleted) -> Self {
        Self::ItemCompleted(value)
    }
}
impl ::std::convert::From<TurnCompleted> for TranscriptEvent {
    fn from(value: TurnCompleted) -> Self {
        Self::TurnCompleted(value)
    }
}
impl ::std::convert::From<OutboxFailed> for TranscriptEvent {
    fn from(value: OutboxFailed) -> Self {
        Self::OutboxFailed(value)
    }
}
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug)]
pub struct TurnCompleted {
    pub data: TurnCompletedData,
    pub seq: u64,
    #[serde(rename = "type")]
    pub type_: TurnCompletedType,
}
impl TurnCompleted {
    pub fn builder() -> builder::TurnCompleted {
        Default::default()
    }
}
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug)]
pub struct TurnCompletedData {
    #[serde(
        rename = "stopReason",
        default,
        skip_serializing_if = "::std::option::Option::is_none"
    )]
    pub stop_reason: ::std::option::Option<::std::string::String>,
}
impl ::std::default::Default for TurnCompletedData {
    fn default() -> Self {
        Self {
            stop_reason: Default::default(),
        }
    }
}
impl TurnCompletedData {
    pub fn builder() -> builder::TurnCompletedData {
        Default::default()
    }
}
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum TurnCompletedType {
    #[serde(rename = "turn.completed")]
    TurnCompleted,
}
impl ::std::fmt::Display for TurnCompletedType {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::TurnCompleted => f.write_str("turn.completed"),
        }
    }
}
impl ::std::str::FromStr for TurnCompletedType {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "turn.completed" => Ok(Self::TurnCompleted),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for TurnCompletedType {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<&::std::string::String> for TurnCompletedType {
    type Error = self::error::ConversionError;
    fn try_from(
        value: &::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for TurnCompletedType {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
