// agentquay/JsonSchemaGenerator.cpp — 从 QMetaMethod 参数类型生成 JSON Schema。
#include "agentquay/JsonSchemaGenerator.h"

#include <QMetaEnum>
#include <QMetaType>
#include <QStringList>
#include <QJsonArray>

namespace agentquay {

namespace {

QJsonObject makeObject(const QString& type)
{
    QJsonObject o;
    o.insert(QStringLiteral("type"), type);
    return o;
}

// 枚举 → { "enum": [key1, key2, ...] }（通过注册的 QMetaObject 取 QMetaEnum）
QJsonObject enumSchema(int metaTypeId)
{
        if (const QMetaObject* mo = QMetaType::metaObjectForType(metaTypeId)) {
        for (int i = 0; i < mo->enumeratorCount(); ++i) {
            const QMetaEnum me = mo->enumerator(i);
            if (!me.isValid() || me.keyCount() == 0)
                continue;
            QJsonArray values;
            for (int k = 0; k < me.keyCount(); ++k)
                values.append(QString::fromLatin1(me.key(k)));
            QJsonObject schema;
            schema.insert(QStringLiteral("enum"), values);
            return schema;
        }
    }
    return QJsonObject();
}

} // namespace

QJsonObject JsonSchemaGenerator::typeSchema(int metaTypeId)
{
    switch (metaTypeId) {
    case QMetaType::QString:
    case QMetaType::QChar:
    case QMetaType::QByteArray:
    case QMetaType::QUrl:
    case QMetaType::QDate:
    case QMetaType::QTime:
    case QMetaType::QDateTime:
        return makeObject(QStringLiteral("string"));
    case QMetaType::Int:
    case QMetaType::UInt:
    case QMetaType::Short:
    case QMetaType::UShort:
    case QMetaType::LongLong:
    case QMetaType::ULongLong:
    case QMetaType::Char:
    case QMetaType::UChar:
        return makeObject(QStringLiteral("integer"));
    case QMetaType::Float:
    case QMetaType::Double:
        return makeObject(QStringLiteral("number"));
    case QMetaType::Bool:
        return makeObject(QStringLiteral("boolean"));
    case QMetaType::QVariantList:
    case QMetaType::QStringList:
    case QMetaType::QJsonArray: {
        QJsonObject o = makeObject(QStringLiteral("array"));
        o.insert(QStringLiteral("items"), QJsonObject());
        return o;
    }
    case QMetaType::QVariantMap:
    case QMetaType::QJsonObject:
        return makeObject(QStringLiteral("object"));
    default: {
        if (QMetaType::isRegistered(metaTypeId)) {
            // 枚举走 QMetaEnum；其余未识别类型不加约束
            const QMetaType mt(metaTypeId);
            if (mt.flags().testFlag(QMetaType::IsEnumeration))
                return enumSchema(metaTypeId);
        }
        return QJsonObject(); // 任意值
    }
    }
}

QJsonObject JsonSchemaGenerator::paramSchema(const QMetaMethod& method)
{
    QJsonObject properties;
    QJsonArray required;

    const QList<QByteArray> paramNames = method.parameterNames();
    for (int i = 0; i < method.parameterCount() && i < paramNames.size(); ++i) {
        const int typeId = method.parameterMetaType(i).id();
        QString name = QString::fromUtf8(paramNames.value(i));
        QJsonObject schema = typeSchema(typeId);
        properties.insert(name, schema);
        // Bridge 按 inputSchema 校验必填；C++ 方法参数均视为必填
        required.append(name);
    }

    QJsonObject root = makeObject(QStringLiteral("object"));
    root.insert(QStringLiteral("properties"), properties);
    if (!required.isEmpty())
        root.insert(QStringLiteral("required"), required);
    return root;
}

} // namespace agentquay