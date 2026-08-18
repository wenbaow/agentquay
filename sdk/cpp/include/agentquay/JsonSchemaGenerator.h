// agentquay/JsonSchemaGenerator.h — 从 QMetaMethod 参数类型生成 JSON Schema
// （设计文档 §4.7 "QJsonDocument + QMetaType 从 C++ 类型生成 JSON Schema"）。
//
// 对齐 Java SDK 的 JsonSchemaGenerator：为每个方法参数生成
// { "type": "object", "properties": { paramName: schema }, "required": [...] }。
//
// 支持的参数类型 → Schema 映射：
//   QString / QByteArray / QChar / QUrl / 日期时间      → { "type": "string" }
//   整数族（int / uint / short / long long ...）        → { "type": "integer" }
//   float / double                                      → { "type": "number" }
//   bool                                                → { "type": "boolean" }
//   QVariantList / QStringList / QJsonArray            → { "type": "array", "items": {} }
//   QVariantMap / QJsonObject                          → { "type": "object" }
//   注册的枚举（Q_ENUM / QMetaType #）                  → { "enum": [...] }
//   其他未识别类型（自定义类，需 Q_DECLARE_METATYPE）   → {}（不加约束，可空）
#pragma once

#include <QJsonObject>
#include <QMetaMethod>

namespace agentquay {

class JsonSchemaGenerator {
public:
    /** 生成方法参数级 inputSchema（含 required 列表）。 */
    static QJsonObject paramSchema(const QMetaMethod& method);

    /** 单个 QMetaType → JSON Schema。 */
    static QJsonObject typeSchema(int metaTypeId);

private:
    JsonSchemaGenerator() = delete;
};

} // namespace agentquay