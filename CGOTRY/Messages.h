#ifndef Messages_h
#define Messages_h
#include "Wrapper.h"
struct msg_request{
    metaData metadoc;
    std::string doc;
};

struct ValueForType{
    int64_t intValue;
    double doubleValue;
    std::string stringValue;
    bool boolValue;
};

struct msg_response{
    int type[20];//SShould change with increase in arguments in emit function
    ValueForType arr[20]; //Should change with increase in arguments in emit function
    int ValueLength;
    int length;
};
#endif
