// SPDX-License-Identifier: MIT
//! Between the contract's JSON metrics and srad's typed metrics.

use serde_json::{json, Value};
use srad::eon::{AliasConfig, BirthInitializer, BirthMetricDetails, MetricToken, PublishMetric};
use srad::types::payload::{metric, DataType};
use srad::types::{DateTime, MetricValue, MetricValueKind};

pub fn datatype(name: &str) -> Option<DataType> {
    Some(match name {
        "Int8" => DataType::Int8,
        "Int16" => DataType::Int16,
        "Int32" => DataType::Int32,
        "Int64" => DataType::Int64,
        "UInt8" => DataType::UInt8,
        "UInt16" => DataType::UInt16,
        "UInt32" => DataType::UInt32,
        "UInt64" => DataType::UInt64,
        "Float" => DataType::Float,
        "Double" => DataType::Double,
        "Boolean" => DataType::Boolean,
        "String" => DataType::String,
        "DateTime" => DataType::DateTime,
        "Text" => DataType::Text,
        _ => return None,
    })
}

pub fn datatype_name(dt: DataType) -> String {
    match dt {
        DataType::UInt8 => "UInt8".into(),
        DataType::UInt16 => "UInt16".into(),
        DataType::UInt32 => "UInt32".into(),
        DataType::UInt64 => "UInt64".into(),
        DataType::DateTime => "DateTime".into(),
        other => format!("{other:?}"),
    }
}

fn float(f: f64) -> Value {
    serde_json::Number::from_f64(f)
        .map(Value::Number)
        .unwrap_or(Value::Null)
}

/// A received value, decoded with the datatype of its birth, as contract JSON.
pub fn to_json(dt: DataType, v: Option<MetricValue>) -> Result<Value, String> {
    let Some(v) = v else { return Ok(Value::Null) };
    let kind = MetricValueKind::try_from_metric_value(dt, v).map_err(|e| e.to_string())?;
    Ok(match kind {
        MetricValueKind::Int8(x) => json!(x),
        MetricValueKind::Int16(x) => json!(x),
        MetricValueKind::Int32(x) => json!(x),
        MetricValueKind::Int64(x) => json!(x),
        MetricValueKind::UInt8(x) => json!(x),
        MetricValueKind::UInt16(x) => json!(x),
        MetricValueKind::UInt32(x) => json!(x),
        MetricValueKind::UInt64(x) => json!(x),
        MetricValueKind::Float(x) => float(x as f64),
        MetricValueKind::Double(x) => float(x),
        MetricValueKind::Boolean(x) => json!(x),
        MetricValueKind::String(x) | MetricValueKind::Text(x) => json!(x),
        MetricValueKind::DateTime(x) => json!(x.date_time),
        other => return Err(format!("unsupported value {other:?}")),
    })
}

/// The datatype a value's protobuf variant suggests, for a metric no birth
/// or fixture describes (commands carry no datatype).
pub fn guess_datatype(v: &Option<MetricValue>) -> DataType {
    match v.as_ref().map(|v| &v.0) {
        Some(metric::Value::BooleanValue(_)) => DataType::Boolean,
        Some(metric::Value::StringValue(_)) => DataType::String,
        Some(metric::Value::FloatValue(_)) => DataType::Float,
        Some(metric::Value::DoubleValue(_)) => DataType::Double,
        Some(metric::Value::LongValue(_)) => DataType::Int64,
        _ => DataType::Int32,
    }
}

/// A metric registered in a birth, of the Rust type of its datatype.
pub enum Tok {
    I8(MetricToken<i8>),
    I16(MetricToken<i16>),
    I32(MetricToken<i32>),
    I64(MetricToken<i64>),
    U8(MetricToken<u8>),
    U16(MetricToken<u16>),
    U32(MetricToken<u32>),
    U64(MetricToken<u64>),
    F32(MetricToken<f32>),
    F64(MetricToken<f64>),
    Bool(MetricToken<bool>),
    Str(MetricToken<String>),
    Dt(MetricToken<DateTime>),
}

/// Registers a fixture metric with its fixture alias and current value.
pub fn register(bi: &mut BirthInitializer, name: &str, dt: &str, alias: u64, v: &Value) -> Result<Tok, String> {
    macro_rules! reg {
        ($variant:ident, $ty:ty, $val:expr, $dtype:expr) => {{
            let val: $ty = $val.ok_or_else(|| format!("{name}: {v} is not a {dt}"))?;
            let details = BirthMetricDetails::<$ty>::new_with_initial_value_explicit_type(name, val, $dtype)
                .map_err(|e| e.to_string())?
                .with_alias_config(AliasConfig::Custom(alias));
            Tok::$variant(bi.register_metric(details).map_err(|e| e.to_string())?)
        }};
    }
    Ok(match dt {
        "Int8" => reg!(I8, i8, v.as_i64().map(|x| x as i8), DataType::Int8),
        "Int16" => reg!(I16, i16, v.as_i64().map(|x| x as i16), DataType::Int16),
        "Int32" => reg!(I32, i32, v.as_i64().map(|x| x as i32), DataType::Int32),
        "Int64" => reg!(I64, i64, v.as_i64(), DataType::Int64),
        "UInt8" => reg!(U8, u8, v.as_u64().map(|x| x as u8), DataType::UInt8),
        "UInt16" => reg!(U16, u16, v.as_u64().map(|x| x as u16), DataType::UInt16),
        "UInt32" => reg!(U32, u32, v.as_u64().map(|x| x as u32), DataType::UInt32),
        "UInt64" => reg!(U64, u64, v.as_u64(), DataType::UInt64),
        "Float" => reg!(F32, f32, v.as_f64().map(|x| x as f32), DataType::Float),
        "Double" => reg!(F64, f64, v.as_f64(), DataType::Double),
        "Boolean" => reg!(Bool, bool, v.as_bool(), DataType::Boolean),
        "String" => reg!(Str, String, v.as_str().map(str::to_string), DataType::String),
        "Text" => reg!(Str, String, v.as_str().map(str::to_string), DataType::Text),
        "DateTime" => reg!(Dt, DateTime, v.as_u64().map(DateTime::new), DataType::DateTime),
        _ => return Err(format!("{name}: unknown datatype {dt}")),
    })
}

/// A data message metric (by alias, as the birth registered it) with value v.
pub fn publish_metric(tok: &Tok, v: &Value) -> Option<PublishMetric> {
    Some(match tok {
        Tok::I8(t) => t.create_publish_metric(Some(v.as_i64()? as i8)),
        Tok::I16(t) => t.create_publish_metric(Some(v.as_i64()? as i16)),
        Tok::I32(t) => t.create_publish_metric(Some(v.as_i64()? as i32)),
        Tok::I64(t) => t.create_publish_metric(Some(v.as_i64()?)),
        Tok::U8(t) => t.create_publish_metric(Some(v.as_u64()? as u8)),
        Tok::U16(t) => t.create_publish_metric(Some(v.as_u64()? as u16)),
        Tok::U32(t) => t.create_publish_metric(Some(v.as_u64()? as u32)),
        Tok::U64(t) => t.create_publish_metric(Some(v.as_u64()?)),
        Tok::F32(t) => t.create_publish_metric(Some(v.as_f64()? as f32)),
        Tok::F64(t) => t.create_publish_metric(Some(v.as_f64()?)),
        Tok::Bool(t) => t.create_publish_metric(Some(v.as_bool()?)),
        Tok::Str(t) => t.create_publish_metric(Some(v.as_str()?.to_string())),
        Tok::Dt(t) => t.create_publish_metric(Some(DateTime::new(v.as_u64()?))),
    })
}

/// A command metric for srad's application client.
pub fn app_metric(id: srad::types::MetricId, dt: &str, v: &Value) -> Option<srad::app::PublishMetric> {
    use srad::app::PublishMetric as P;
    Some(
        match dt {
            "Int8" => P::new(id, v.as_i64()? as i8),
            "Int16" => P::new(id, v.as_i64()? as i16),
            "Int32" => P::new(id, v.as_i64()? as i32),
            "Int64" => P::new(id, v.as_i64()?),
            "UInt8" => P::new(id, v.as_u64()? as u8),
            "UInt16" => P::new(id, v.as_u64()? as u16),
            "UInt32" => P::new(id, v.as_u64()? as u32),
            "UInt64" => P::new(id, v.as_u64()?),
            "Float" => P::new(id, v.as_f64()? as f32),
            "Double" => P::new(id, v.as_f64()?),
            "Boolean" => P::new(id, v.as_bool()?),
            "String" | "Text" => P::new(id, v.as_str()?.to_string()),
            "DateTime" => P::new(id, DateTime::new(v.as_u64()?)),
            _ => return None,
        }
        .timestamp(crate::out::ms()),
    )
}
